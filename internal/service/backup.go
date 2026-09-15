package service

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// BackupStore makes a consistent copy of the database.
type BackupStore interface {
	Backup(ctx context.Context, path string) error
}

// BackupSender delivers a backup file to the owner.
type BackupSender interface {
	SendBackup(ctx context.Context, path, caption string) error
}

// BackupInfo describes a backup file on disk.
type BackupInfo struct {
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"created_at"`
}

const (
	backupPrefix = "tgtriage-"
	backupSuffix = ".db.gz"
	// telegramUploadLimit is the Bot API limit for sendDocument.
	telegramUploadLimit = 50 << 20
)

// BackupService makes gzip-compressed database snapshots on schedule, keeps the latest N and sends
// each one to the owner in Telegram.
type BackupService struct {
	store    BackupStore
	dir      string
	settings *SettingsService
	sender   BackupSender
	log      *slog.Logger
	mu       sync.Mutex
}

func NewBackupService(store BackupStore, dir string, settings *SettingsService, sender BackupSender, log *slog.Logger) *BackupService {
	return &BackupService{store: store, dir: dir, settings: settings, sender: sender, log: log.With("component", "backup")}
}

// Dir returns the backup directory.
func (s *BackupService) Dir() string { return s.dir }

// RunIfDue makes the daily backup within 6 hours after the configured time.
func (s *BackupService) RunIfDue(ctx context.Context) {
	st := s.settings.Get().Backup
	if !st.Enabled {
		return
	}
	h, m, err := ParseClock(st.Time)
	if err != nil {
		return
	}
	loc := s.settings.Location()
	now := time.Now().In(loc)
	target := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, loc)
	if now.Before(target) || now.Sub(target) > 6*time.Hour {
		return
	}
	today := now.Format(time.DateOnly)
	if last, err := s.settings.Meta(ctx, "last_backup"); err != nil || last == today {
		return
	}
	// mark first: a failing backup must not retry every scheduler tick
	if err := s.settings.SetMeta(ctx, "last_backup", today); err != nil {
		s.log.Error("save backup mark", "err", err)
		return
	}
	if _, err := s.Run(ctx); err != nil {
		s.log.Error("scheduled backup failed", "err", err)
	}
}

// Run makes a backup now, rotates old ones and sends it to Telegram when enabled.
func (s *BackupService) Run(ctx context.Context) (BackupInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.settings.Get().Backup
	if err := os.MkdirAll(s.dir, 0o750); err != nil {
		return BackupInfo{}, fmt.Errorf("create backup dir: %w", err)
	}
	now := time.Now().In(s.settings.Location())
	name := backupPrefix + now.Format("20060102-150405") + backupSuffix
	final := filepath.Join(s.dir, name)
	raw := filepath.Join(s.dir, ".tmp-"+now.Format("20060102-150405")+".db")
	_ = os.Remove(raw)
	defer os.Remove(raw)

	started := time.Now()
	if err := s.store.Backup(ctx, raw); err != nil {
		return BackupInfo{}, err
	}
	if err := gzipFile(raw, final+".part"); err != nil {
		_ = os.Remove(final + ".part")
		return BackupInfo{}, err
	}
	if err := os.Rename(final+".part", final); err != nil {
		return BackupInfo{}, err
	}
	fi, err := os.Stat(final)
	if err != nil {
		return BackupInfo{}, err
	}
	info := BackupInfo{Name: name, Size: fi.Size(), CreatedAt: now}
	s.log.Info("backup created", "file", name, "size", fi.Size(), "took", time.Since(started).Round(time.Millisecond))
	s.rotate(max(st.Keep, 1))

	if st.SendTelegram && s.sender != nil {
		if fi.Size() >= telegramUploadLimit {
			s.log.Warn("backup is too large for Telegram, kept on disk only", "size", fi.Size())
		} else {
			caption := fmt.Sprintf("💾 Бэкап базы · %s · %s", now.Format("02.01.2006 15:04"), humanSize(fi.Size()))
			if err := s.sender.SendBackup(ctx, final, caption); err != nil {
				s.log.Error("send backup to Telegram", "err", err)
				return info, fmt.Errorf("бэкап сохранён, но не отправлен в Telegram: %w", err)
			}
		}
	}
	return info, nil
}

// List returns backups on disk, newest first.
func (s *BackupService) List() ([]BackupInfo, error) {
	entries, err := os.ReadDir(s.dir)
	if os.IsNotExist(err) {
		return []BackupInfo{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []BackupInfo{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), backupPrefix) || !strings.HasSuffix(e.Name(), backupSuffix) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, BackupInfo{Name: e.Name(), Size: fi.Size(), CreatedAt: fi.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out, nil
}

func (s *BackupService) rotate(keep int) {
	list, err := s.List()
	if err != nil {
		s.log.Warn("list backups for rotation", "err", err)
		return
	}
	for i := keep; i < len(list); i++ {
		if err := os.Remove(filepath.Join(s.dir, list[i].Name)); err != nil {
			s.log.Warn("remove old backup", "file", list[i].Name, "err", err)
		}
	}
}

func gzipFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	zw, err := gzip.NewWriterLevel(out, gzip.BestCompression)
	if err != nil {
		_ = out.Close()
		return err
	}
	zw.Name = strings.TrimSuffix(filepath.Base(dst), ".gz.part")
	if _, err := io.Copy(zw, in); err != nil {
		_ = zw.Close()
		_ = out.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f МБ", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d КБ", n>>10)
	default:
		return fmt.Sprintf("%d Б", n)
	}
}
