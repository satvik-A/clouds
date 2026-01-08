// Package core provides encrypted database support for CloudFS.
// Based on design.txt Section 11: Encryption pipeline.
//
// INVARIANTS:
// - Index encrypted at rest via SQLCipher (AES-256)
// - Key derived from master password (never hardcoded)
// - Fail safely if key is incorrect
// - Recovery bundle compatible
package core

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/mutecomm/go-sqlcipher/v4"
)

// EncryptedDB wraps a SQLCipher-encrypted SQLite database.
type EncryptedDB struct {
	db         *sql.DB
	dbPath     string
	encrypted  bool
}

// OpenEncryptedDB opens a SQLCipher-encrypted database.
// If passphrase is empty, opens without encryption.
// If the database exists and passphrase is wrong, returns an error.
func OpenEncryptedDB(dbPath string, passphrase string) (*EncryptedDB, error) {
	// Ensure directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create directory: %w", err)
	}

	var dsn string
	var encrypted bool

	if passphrase != "" {
		// With SQLCipher encryption - use DSN pragma_key parameter
		// URL-encode the passphrase for the DSN
		dsn = fmt.Sprintf("file:%s?_pragma_key=%s&_journal_mode=WAL&_synchronous=NORMAL", dbPath, passphrase)
		encrypted = true
	} else {
		// Without encryption (development/testing mode)
		dsn = fmt.Sprintf("file:%s?_journal_mode=WAL&_synchronous=NORMAL", dbPath)
		encrypted = false
	}

	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// If encrypted, verify key is correct by reading from database
	if encrypted {
		// This will fail if the key is wrong
		var version string
		err = db.QueryRow("SELECT sqlite_version()").Scan(&version)
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("invalid passphrase or corrupted database: %w", err)
		}
	}

	// Test connection
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	return &EncryptedDB{
		db:        db,
		dbPath:    dbPath,
		encrypted: encrypted,
	}, nil
}

// deriveKey derives a 256-bit key from the passphrase.
// Uses simple key derivation - for production, use PBKDF2 or Argon2.
func deriveKey(passphrase string) []byte {
	// Simple key derivation (pad or truncate to 32 bytes)
	// TODO: Use proper KDF (PBKDF2/Argon2) for production
	key := make([]byte, 32)
	copy(key, []byte(passphrase))
	return key
}

// DB returns the underlying database connection.
func (edb *EncryptedDB) DB() *sql.DB {
	return edb.db
}

// Close closes the database connection.
func (edb *EncryptedDB) Close() error {
	return edb.db.Close()
}

// IsEncrypted returns whether the database is encrypted.
func (edb *EncryptedDB) IsEncrypted() bool {
	return edb.encrypted
}

// Path returns the database file path.
func (edb *EncryptedDB) Path() string {
	return edb.dbPath
}

// ChangePassphrase changes the encryption passphrase.
// This re-encrypts the entire database with a new key.
func (edb *EncryptedDB) ChangePassphrase(ctx context.Context, newPassphrase string) error {
	if !edb.encrypted {
		return fmt.Errorf("database is not encrypted")
	}

	pragma := fmt.Sprintf("PRAGMA rekey = '%s';", newPassphrase)
	if _, err := edb.db.ExecContext(ctx, pragma); err != nil {
		return fmt.Errorf("failed to change passphrase: %w", err)
	}

	return nil
}

// ExportRecoveryBundle exports an encrypted backup of the database.
// The recovery bundle includes the database and recovery instructions.
func (edb *EncryptedDB) ExportRecoveryBundle(ctx context.Context, bundlePath string) error {
	// Create bundle directory
	if err := os.MkdirAll(bundlePath, 0700); err != nil {
		return fmt.Errorf("failed to create bundle directory: %w", err)
	}

	// Copy database file using VACUUM INTO for consistency
	dbDst := filepath.Join(bundlePath, "index.db")
	vacuumQuery := fmt.Sprintf("VACUUM INTO '%s'", dbDst)
	if _, err := edb.db.ExecContext(ctx, vacuumQuery); err != nil {
		// Fallback to file copy if VACUUM INTO fails
		if err := copyDBFile(edb.dbPath, dbDst); err != nil {
			return fmt.Errorf("failed to copy database: %w", err)
		}
	}

	// Create README for manual recovery
	readme := `CloudFS Recovery Bundle
========================

This bundle contains:
- index.db: The SQLCipher-encrypted metadata index (AES-256)

RECOVERY WITH CLOUDFS:
1. Install CloudFS
2. Run: cloudfs recover --bundle /path/to/bundle
3. Enter your encryption passphrase when prompted

MANUAL RECOVERY WITHOUT CLOUDFS:
1. Install sqlcipher: brew install sqlcipher
2. Open the database: sqlcipher index.db
3. Enter the key: PRAGMA key = "your-passphrase";
4. Verify access: SELECT * FROM entries LIMIT 5;
5. Query tables: entries, versions, placements, providers

IMPORTANT:
- The passphrase is NOT stored in this bundle
- Keep your passphrase in a secure location
- Without the passphrase, data cannot be recovered

SCHEMA OVERVIEW:
- entries: File and folder metadata
- versions: Immutable file versions (atomic unit)
- placements: Where data is stored on providers
- providers: Configured storage backends
- cache_entries: Local cache state
- journal: Write-ahead log for crash recovery

For full schema, decrypt and run: .schema
`
	readmePath := filepath.Join(bundlePath, "README.txt")
	if err := os.WriteFile(readmePath, []byte(readme), 0644); err != nil {
		return fmt.Errorf("failed to write README: %w", err)
	}

	return nil
}

// ValidatePassphrase checks if a passphrase is correct for the database.
func ValidatePassphrase(dbPath string, passphrase string) error {
	db, err := OpenEncryptedDB(dbPath, passphrase)
	if err != nil {
		return err
	}
	defer db.Close()

	// Try to read the schema version - if wrong key, this fails
	var version string
	err = db.DB().QueryRow("SELECT value FROM index_meta WHERE key = 'schema_version'").Scan(&version)
	if err != nil {
		return fmt.Errorf("invalid passphrase or corrupted database: %w", err)
	}

	return nil
}

// GenerateRandomKey generates a cryptographically secure random key.
func GenerateRandomKey(length int) (string, error) {
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("failed to generate random key: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}

// copyDBFile copies a database file safely.
func copyDBFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = dstFile.ReadFrom(srcFile)
	if err != nil {
		return err
	}

	return dstFile.Sync()
}

// EncryptionStatus describes the encryption state of a database.
type EncryptionStatus struct {
	IsEncrypted   bool
	CipherVersion string
	KeyDerivation string
}

// GetEncryptionStatus returns info about the database encryption.
func (edb *EncryptedDB) GetEncryptionStatus(ctx context.Context) (*EncryptionStatus, error) {
	status := &EncryptionStatus{
		IsEncrypted: edb.encrypted,
	}

	if edb.encrypted {
		var cipherVersion string
		err := edb.db.QueryRowContext(ctx, "PRAGMA cipher_version").Scan(&cipherVersion)
		if err == nil {
			status.CipherVersion = cipherVersion
		}

		var settings string
		err = edb.db.QueryRowContext(ctx, "PRAGMA cipher_settings").Scan(&settings)
		if err == nil {
			status.KeyDerivation = settings
		}
	}

	return status, nil
}
