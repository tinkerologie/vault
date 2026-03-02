package engine

type BackupItem struct {
	Name     string         `json:"name"`
	Type     string         `json:"type"` // "container", "vm", or "folder"
	Settings map[string]any `json:"settings"`
}

type BackupResult struct {
	ItemName string       `json:"item_name"`
	Success  bool         `json:"success"`
	Error    string       `json:"error"`
	Files    []BackupFile `json:"files"`
}

type BackupFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type ProgressFunc func(item string, percent int, message string)

type Handler interface {
	Backup(item BackupItem, dest string, progress ProgressFunc) (*BackupResult, error)
	Restore(item BackupItem, source string, progress ProgressFunc) error
	ListItems() ([]BackupItem, error)
}
