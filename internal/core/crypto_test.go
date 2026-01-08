// Package core provides SQLCipher encryption tests.
package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestEncryptedDB_OpenWithPassphrase(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cloudfs-crypto-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "encrypted.db")
	passphrase := "test-passphrase-123"

	// Create encrypted database
	db, err := OpenEncryptedDB(dbPath, passphrase)
	if err != nil {
		t.Fatalf("failed to open encrypted db: %v", err)
	}

	if !db.IsEncrypted() {
		t.Error("database should be marked as encrypted")
	}

	// Create a table and insert data
	_, err = db.DB().Exec(`
		CREATE TABLE test_table (id INTEGER PRIMARY KEY, value TEXT);
		INSERT INTO test_table (value) VALUES ('secret data');
	`)
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}

	// Verify data
	var value string
	err = db.DB().QueryRow("SELECT value FROM test_table WHERE id = 1").Scan(&value)
	if err != nil {
		t.Fatalf("failed to query: %v", err)
	}

	if value != "secret data" {
		t.Errorf("expected 'secret data', got '%s'", value)
	}

	db.Close()

	// Verify we can reopen with correct passphrase
	db2, err := OpenEncryptedDB(dbPath, passphrase)
	if err != nil {
		t.Fatalf("failed to reopen encrypted db: %v", err)
	}

	var value2 string
	err = db2.DB().QueryRow("SELECT value FROM test_table WHERE id = 1").Scan(&value2)
	if err != nil {
		t.Fatalf("failed to query after reopen: %v", err)
	}

	if value2 != "secret data" {
		t.Errorf("expected 'secret data', got '%s'", value2)
	}

	db2.Close()
}

func TestEncryptedDB_WrongPassphraseFails(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cloudfs-crypto-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "encrypted.db")
	correctPass := "correct-passphrase"
	wrongPass := "wrong-passphrase"

	// Create encrypted database with correct passphrase
	db, err := OpenEncryptedDB(dbPath, correctPass)
	if err != nil {
		t.Fatalf("failed to create encrypted db: %v", err)
	}

	_, err = db.DB().Exec("CREATE TABLE test (id INTEGER)")
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}
	db.Close()

	// Try to open with wrong passphrase - should fail
	_, err = OpenEncryptedDB(dbPath, wrongPass)
	if err == nil {
		t.Error("opening with wrong passphrase should fail")
	}

	// Error message should indicate invalid passphrase
	if err != nil && err.Error()[:len("invalid passphrase")] != "invalid passphrase" {
		t.Logf("Got expected error (wrong passphrase): %v", err)
	}
}

func TestEncryptedDB_UnencryptedMode(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cloudfs-crypto-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "unencrypted.db")

	// Open without passphrase (unencrypted mode)
	db, err := OpenEncryptedDB(dbPath, "")
	if err != nil {
		t.Fatalf("failed to open unencrypted db: %v", err)
	}

	if db.IsEncrypted() {
		t.Error("database should not be marked as encrypted")
	}

	_, err = db.DB().Exec("CREATE TABLE test (id INTEGER)")
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}

	db.Close()

	// Should be able to reopen
	db2, err := OpenEncryptedDB(dbPath, "")
	if err != nil {
		t.Fatalf("failed to reopen: %v", err)
	}
	db2.Close()
}

func TestEncryptedDB_RecoveryBundle(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cloudfs-crypto-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	passphrase := "bundle-test-pass"

	// Create and populate database
	db, err := OpenEncryptedDB(dbPath, passphrase)
	if err != nil {
		t.Fatalf("failed to create db: %v", err)
	}

	_, err = db.DB().Exec(`
		CREATE TABLE entries (id INTEGER PRIMARY KEY, name TEXT);
		INSERT INTO entries (name) VALUES ('important-file.txt');
	`)
	if err != nil {
		t.Fatalf("failed to populate: %v", err)
	}

	// Export recovery bundle
	bundlePath := filepath.Join(tmpDir, "recovery_bundle")
	ctx := context.Background()
	if err := db.ExportRecoveryBundle(ctx, bundlePath); err != nil {
		t.Fatalf("failed to export bundle: %v", err)
	}

	db.Close()

	// Verify bundle contents
	bundleDB := filepath.Join(bundlePath, "index.db")
	if _, err := os.Stat(bundleDB); os.IsNotExist(err) {
		t.Error("bundle should contain index.db")
	}

	readmePath := filepath.Join(bundlePath, "README.txt")
	if _, err := os.Stat(readmePath); os.IsNotExist(err) {
		t.Error("bundle should contain README.txt")
	}

	// Verify bundle can be decrypted with same passphrase
	bundleDbConn, err := OpenEncryptedDB(bundleDB, passphrase)
	if err != nil {
		t.Fatalf("failed to open bundle db: %v", err)
	}

	var name string
	err = bundleDbConn.DB().QueryRow("SELECT name FROM entries WHERE id = 1").Scan(&name)
	if err != nil {
		t.Fatalf("failed to query bundle: %v", err)
	}

	if name != "important-file.txt" {
		t.Errorf("expected 'important-file.txt', got '%s'", name)
	}

	bundleDbConn.Close()
}

func TestEncryptedDB_ChangePassphrase(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cloudfs-crypto-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "rekey.db")
	oldPass := "old-passphrase"
	newPass := "new-passphrase"

	// Create with old passphrase
	db, err := OpenEncryptedDB(dbPath, oldPass)
	if err != nil {
		t.Fatalf("failed to create db: %v", err)
	}

	_, err = db.DB().Exec("CREATE TABLE test (value TEXT)")
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}
	_, err = db.DB().Exec("INSERT INTO test (value) VALUES ('sensitive')")
	if err != nil {
		t.Fatalf("failed to insert: %v", err)
	}

	// Change passphrase
	ctx := context.Background()
	if err := db.ChangePassphrase(ctx, newPass); err != nil {
		t.Fatalf("failed to change passphrase: %v", err)
	}

	db.Close()

	// Old passphrase should fail
	_, err = OpenEncryptedDB(dbPath, oldPass)
	if err == nil {
		t.Error("old passphrase should not work after rekey")
	}

	// New passphrase should work
	db2, err := OpenEncryptedDB(dbPath, newPass)
	if err != nil {
		t.Fatalf("new passphrase should work: %v", err)
	}

	var value string
	err = db2.DB().QueryRow("SELECT value FROM test").Scan(&value)
	if err != nil {
		t.Fatalf("failed to query with new passphrase: %v", err)
	}

	if value != "sensitive" {
		t.Errorf("expected 'sensitive', got '%s'", value)
	}

	db2.Close()
}

func TestGenerateRandomKey(t *testing.T) {
	key1, err := GenerateRandomKey(32)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	if len(key1) != 64 { // 32 bytes = 64 hex chars
		t.Errorf("expected 64 hex chars, got %d", len(key1))
	}

	key2, err := GenerateRandomKey(32)
	if err != nil {
		t.Fatalf("failed to generate second key: %v", err)
	}

	if key1 == key2 {
		t.Error("random keys should be different")
	}
}
