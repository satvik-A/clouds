# CloudFS TUI Keymap

## Global Keys (Available in all views)

| Key         | Action          | Description                                   |
| ----------- | --------------- | --------------------------------------------- |
| `q` / `Q`   | Quit            | Exit TUI (with confirmation if tasks running) |
| `?` / `F1`  | Help            | Show help overlay                             |
| `Esc`       | Back/Cancel     | Close modal, cancel action, go back           |
| `Tab`       | Next Pane       | Cycle focus between panes                     |
| `Shift+Tab` | Prev Pane       | Cycle focus backwards                         |
| `Ctrl+r`    | Refresh         | Force refresh all data from DB                |
| `Ctrl+e`    | Export          | Export current view as JSON                   |
| `/`         | Search          | Open search/filter input                      |
| `1-9`       | Quick Nav       | Jump to view by number                        |
| `Ctrl+p`    | Command Palette | Open command palette                          |

## Navigation Keys

| Key          | Action                       |
| ------------ | ---------------------------- |
| `j` / `↓`    | Move down                    |
| `k` / `↑`    | Move up                      |
| `h` / `←`    | Collapse / Left              |
| `l` / `→`    | Expand / Right               |
| `g` / `Home` | Go to top                    |
| `G` / `End`  | Go to bottom                 |
| `Ctrl+d`     | Page down                    |
| `Ctrl+u`     | Page up                      |
| `Enter`      | Select / Open                |
| `Space`      | Toggle select (multi-select) |

## View-Specific Keys

### Dashboard View (`1`)

| Key | Action                |
| --- | --------------------- |
| `r` | Refresh stats         |
| `o` | Open overview JSON    |
| `p` | Provider quick status |

### Files View (`2`)

| Key      | Action     | Description                    |
| -------- | ---------- | ------------------------------ |
| `H`      | Hydrate    | Download selected file(s)      |
| `D`      | Dehydrate  | Remove local, keep placeholder |
| `P`      | Pin/Unpin  | Toggle cache pin               |
| `A`      | Archive    | Create cold archive            |
| `R`      | Restore    | Restore from archive           |
| `S`      | Snapshot   | Create snapshot                |
| `T`      | Trash      | Move to trash                  |
| `X`      | Explain    | Show detailed explanation      |
| `V`      | Verify     | Verify file integrity          |
| `C`      | Cache      | Open cache actions             |
| `i`      | Info       | Show file details              |
| `y`      | Copy path  | Copy path to clipboard         |
| `n`      | New        | Add new file/folder            |
| `Ctrl+a` | Select All | Select all visible items       |

### Providers View (`3`)

| Key | Action          |
| --- | --------------- |
| `a` | Add provider    |
| `d` | Delete provider |
| `e` | Edit provider   |
| `t` | Test connection |
| `s` | Show status     |
| `p` | Set priority    |

### Archives View (`4`)

| Key | Action          |
| --- | --------------- |
| `v` | Verify archive  |
| `r` | Restore archive |
| `p` | Repair (PAR2)   |
| `i` | Inspect chunks  |
| `d` | Delete archive  |

### Snapshots View (`5`)

| Key | Action           |
| --- | ---------------- |
| `c` | Create snapshot  |
| `r` | Restore snapshot |
| `d` | Delete snapshot  |
| `f` | Show diff        |
| `p` | Preview restore  |

### Trash View (`6`)

| Key | Action                   |
| --- | ------------------------ |
| `r` | Restore item             |
| `p` | Purge selected           |
| `P` | Purge all (with confirm) |
| `i` | Item info                |

### Journal View (`7`)

| Key | Action             |
| --- | ------------------ |
| `r` | Resume operation   |
| `R` | Rollback operation |
| `i` | Inspect payload    |
| `c` | Clear completed    |

### Queue View (`8`)

| Key | Action         |
| --- | -------------- |
| `p` | Push request   |
| `l` | Pull request   |
| `c` | Cancel request |
| `r` | Retry failed   |

### Health View (`9`)

| Key | Action          |
| --- | --------------- |
| `r` | Refresh health  |
| `f` | Filter by risk  |
| `v` | Verify selected |

### Cache View (`0`)

| Key | Action             |
| --- | ------------------ |
| `e` | Evict selected     |
| `E` | Evict all unpinned |
| `p` | Pin/Unpin          |
| `s` | Sort by size/LRU   |
| `l` | Preload            |

## Modal Keys

### Confirmation Dialog

| Key           | Action       |
| ------------- | ------------ |
| `y` / `Enter` | Confirm      |
| `n` / `Esc`   | Cancel       |
| `d`           | Show dry-run |

### Progress Modal

| Key   | Action               |
| ----- | -------------------- |
| `Esc` | Cancel operation     |
| `b`   | Background (hide)    |
| `p`   | Pause (if supported) |

### Search/Filter

| Key      | Action          |
| -------- | --------------- |
| `Enter`  | Apply filter    |
| `Esc`    | Clear and close |
| `Ctrl+u` | Clear input     |

## Vim-Compatible Motions

| Motion | Action               |
| ------ | -------------------- |
| `gg`   | Go to first item     |
| `G`    | Go to last item      |
| `{n}j` | Move down n items    |
| `{n}k` | Move up n items      |
| `zz`   | Center selected item |
| `zt`   | Selected to top      |
| `zb`   | Selected to bottom   |

## Quick Reference Card

```
╔═══════════════════════════════════════════════════════════════════╗
║                    CloudFS TUI Quick Reference                    ║
╠═══════════════════════════════════════════════════════════════════╣
║  NAVIGATION           │  FILE ACTIONS          │  GLOBAL          ║
║  ─────────────────────┼────────────────────────┼─────────────────  ║
║  j/k  ↑/↓  Move       │  H  Hydrate            │  ?  Help         ║
║  h/l  ←/→  Expand     │  D  Dehydrate          │  q  Quit         ║
║  g/G  Top/Bottom      │  P  Pin/Unpin          │  /  Search       ║
║  Tab  Next pane       │  A  Archive            │  Esc Back        ║
║  1-9  Jump to view    │  T  Trash              │  Ctrl+r Refresh  ║
║                       │  X  Explain            │                  ║
╚═══════════════════════════════════════════════════════════════════╝
```
