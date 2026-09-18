// SPDX-License-Identifier: MIT
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/mudler/LocalAI/core/services/advisorylock"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var (
	ErrChatJobNotFound = errors.New("chat job not found")
	ErrChatJobExpired  = errors.New("chat job expired")
)

type ChatJobError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ChatJob struct {
	ID             string        `json:"job_id" gorm:"primaryKey;size:36"`
	Owner          string        `json:"-" gorm:"index"`
	Agent          string        `json:"agent" gorm:"index"`
	MessageID      string        `json:"message_id"`
	ConversationID string        `json:"conversation_id"`
	Status         string        `json:"status"`
	Result         *string       `json:"result,omitempty"`
	Error          *ChatJobError `json:"error,omitempty" gorm:"serializer:json"`
	CreatedAt      time.Time     `json:"created_at"`
	UpdatedAt      time.Time     `json:"updated_at"`
	CompletedAt    *time.Time    `json:"completed_at,omitempty"`
	ExpiresAt      *time.Time    `json:"expires_at,omitempty"`
}

func (ChatJob) TableName() string { return "agent_chat_jobs" }

type ChatStore interface {
	Create(ChatJob) error
	Update(ChatJob) error
	Get(owner, agent, id string) (ChatJob, error)
	Reconcile() error
	Cleanup() error
}

type chatStore struct {
	mu        sync.Mutex
	db        *gorm.DB
	path      string
	retention time.Duration
	now       func() time.Time
}

// Owner must be persisted separately because the public record deliberately omits it.
type chatFileRecord struct {
	ChatJob
	StoredOwner string `json:"owner"`
}

func NewChatStore(db *gorm.DB, directory string, retention time.Duration) (ChatStore, error) {
	if retention <= 0 {
		return nil, errors.New("chat job retention must be positive")
	}
	if db != nil {
		// SQL interpolation can otherwise expose private responses on write failures.
		db = db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
	}
	s := &chatStore{db: db, retention: retention, now: time.Now}
	if db != nil {
		if err := advisorylock.WithLockCtx(context.Background(), db, advisorylock.KeySchemaMigrate, func() error { return db.AutoMigrate(&ChatJob{}) }); err != nil {
			return nil, fmt.Errorf("migrating chat jobs: %w", err)
		}
	} else {
		if directory == "" {
			return nil, errors.New("chat job directory is required")
		}
		if err := os.MkdirAll(directory, 0700); err != nil {
			return nil, err
		}
		if err := os.Chmod(directory, 0700); err != nil {
			return nil, err
		}
		s.path = filepath.Join(directory, "chat-jobs.json")
		if err := os.Chmod(s.path, 0600); errors.Is(err, os.ErrNotExist) {
			if err := s.writeFile(nil); err != nil {
				return nil, err
			}
		} else if err != nil {
			return nil, err
		}
		if _, err := s.readFile(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func terminalChatStatus(status string) bool {
	return status == "completed" || status == "failed" || status == "interrupted" || status == "expired"
}

func validateChatJob(job ChatJob) error {
	if job.ID == "" || job.Agent == "" {
		return errors.New("invalid chat job identity")
	}
	switch job.Status {
	case "accepted", "running", "waiting_user", "waiting_agents", "completed", "failed", "interrupted", "expired":
	default:
		return errors.New("invalid chat job status")
	}
	if terminalChatStatus(job.Status) && job.CompletedAt == nil {
		return errors.New("terminal chat job lacks completion time")
	}
	return nil
}

func (s *chatStore) prepare(job ChatJob) ChatJob {
	now := s.now().UTC()
	if job.CreatedAt.IsZero() {
		job.CreatedAt = now
	}
	job.UpdatedAt = now
	if terminalChatStatus(job.Status) {
		if job.CompletedAt == nil {
			job.CompletedAt = &now
		}
		expires := job.CompletedAt.Add(s.retention)
		job.ExpiresAt = &expires
	} else {
		job.CompletedAt = nil
		job.ExpiresAt = nil
		job.Result = nil
		job.Error = nil
	}
	return job
}

func (s *chatStore) readFile() ([]ChatJob, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil, err
	}
	var records []chatFileRecord
	if err = json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("reading chat jobs: %w", err)
	}
	if records == nil {
		return nil, errors.New("invalid chat jobs file")
	}
	jobs := make([]ChatJob, 0, len(records))
	seen := map[string]bool{}
	for _, record := range records {
		job := record.ChatJob
		job.Owner = record.StoredOwner
		if err := validateChatJob(job); err != nil {
			return nil, err
		}
		if seen[job.ID] {
			return nil, errors.New("duplicate chat job identity")
		}
		seen[job.ID] = true
		jobs = append(jobs, job)
	}
	return jobs, nil
}

func (s *chatStore) writeFile(jobs []ChatJob) error {
	records := make([]chatFileRecord, 0, len(jobs))
	for _, job := range jobs {
		records = append(records, chatFileRecord{ChatJob: job, StoredOwner: job.Owner})
	}
	data, err := json.Marshal(records)
	if err != nil {
		return err
	}
	directory := filepath.Dir(s.path)
	file, err := os.CreateTemp(directory, ".chat-jobs-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err = file.Chmod(0600); err != nil {
		return err
	}
	if _, err = file.Write(data); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(file.Name(), s.path); err != nil {
		return err
	}
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (s *chatStore) Create(job ChatJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	job = s.prepare(job)
	if err := validateChatJob(job); err != nil {
		return err
	}
	if s.db != nil {
		return s.db.Create(&job).Error
	}
	jobs, err := s.readFile()
	if err != nil {
		return err
	}
	for _, existing := range jobs {
		if existing.ID == job.ID {
			return errors.New("chat job already exists")
		}
	}
	return s.writeFile(append(jobs, job))
}

func (s *chatStore) expiry(job ChatJob) error {
	if !terminalChatStatus(job.Status) {
		return nil
	}
	if job.CompletedAt == nil {
		return errors.New("terminal chat job lacks completion time")
	}
	expires := job.CompletedAt.Add(s.retention)
	if !s.now().Before(expires.Add(s.retention)) {
		return ErrChatJobNotFound
	}
	if job.Status == "expired" || !s.now().Before(expires) {
		return ErrChatJobExpired
	}
	return nil
}

func (s *chatStore) get(owner, agent, id string) (ChatJob, error) {
	var job ChatJob
	if s.db != nil {
		err := s.db.Where("owner = ? AND agent = ? AND id = ?", owner, agent, id).First(&job).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return job, ErrChatJobNotFound
		}
		return job, err
	}
	jobs, err := s.readFile()
	if err != nil {
		return job, err
	}
	for _, candidate := range jobs {
		if candidate.ID == id && candidate.Owner == owner && candidate.Agent == agent {
			return candidate, nil
		}
	}
	return job, ErrChatJobNotFound
}

func (s *chatStore) Get(owner, agent, id string) (ChatJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, err := s.get(owner, agent, id)
	if err != nil {
		return ChatJob{}, err
	}
	if err = s.expiry(job); err != nil {
		return ChatJob{}, err
	}
	if terminalChatStatus(job.Status) {
		expires := job.CompletedAt.Add(s.retention)
		job.ExpiresAt = &expires
	}
	return job, nil
}

func (s *chatStore) Update(job ChatJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, err := s.get(job.Owner, job.Agent, job.ID)
	if err != nil {
		return err
	}
	if err = s.expiry(previous); err != nil {
		return err
	}
	if terminalChatStatus(previous.Status) {
		return errors.New("chat job is already terminal")
	}
	job.CreatedAt = previous.CreatedAt
	job.MessageID = previous.MessageID
	job.ConversationID = previous.ConversationID
	job = s.prepare(job)
	if err = validateChatJob(job); err != nil {
		return err
	}
	if s.db != nil {
		result := s.db.Model(&ChatJob{}).Where("owner = ? AND agent = ? AND id = ? AND status = ?", job.Owner, job.Agent, job.ID, previous.Status).Select("*").Updates(&job)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("chat job changed during update")
		}
		return nil
	}
	jobs, err := s.readFile()
	if err != nil {
		return err
	}
	for i := range jobs {
		if jobs[i].ID == job.ID {
			jobs[i] = job
			return s.writeFile(jobs)
		}
	}
	return ErrChatJobNotFound
}

func (s *chatStore) maintain(reconcile bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	mutate := func(jobs []ChatJob) ([]ChatJob, error) {
		kept := make([]ChatJob, 0, len(jobs))
		for _, job := range jobs {
			if err := validateChatJob(job); err != nil {
				return nil, err
			}
			if reconcile && !terminalChatStatus(job.Status) {
				job.Status = "interrupted"
				job.Result = nil
				job.Error = &ChatJobError{Code: "execution_interrupted", Message: "Execution was interrupted; its outcome is unknown."}
				job = s.prepare(job)
			}
			if !reconcile {
				switch s.expiry(job) {
				case ErrChatJobNotFound:
					continue
				case ErrChatJobExpired:
					job.Status = "expired"
					job.Result = nil
					job.Error = nil
					job.MessageID = ""
					job.ConversationID = ""
				}
			}
			kept = append(kept, job)
		}
		return kept, nil
	}
	if s.db != nil {
		return s.db.Transaction(func(tx *gorm.DB) error {
			var jobs []ChatJob
			if err := tx.Find(&jobs).Error; err != nil {
				return err
			}
			kept, err := mutate(jobs)
			if err != nil {
				return err
			}
			ids := map[string]bool{}
			for _, job := range kept {
				ids[job.ID] = true
				if err := tx.Model(&ChatJob{}).Where("id = ? AND owner = ? AND agent = ?", job.ID, job.Owner, job.Agent).Select("*").Updates(&job).Error; err != nil {
					return err
				}
			}
			for _, job := range jobs {
				if !ids[job.ID] {
					if err := tx.Where("id = ? AND owner = ? AND agent = ?", job.ID, job.Owner, job.Agent).Delete(&ChatJob{}).Error; err != nil {
						return err
					}
				}
			}
			return nil
		})
	}
	jobs, err := s.readFile()
	if err != nil {
		return err
	}
	kept, err := mutate(jobs)
	if err != nil {
		return err
	}
	return s.writeFile(kept)
}

func (s *chatStore) Reconcile() error { return s.maintain(true) }
func (s *chatStore) Cleanup() error   { return s.maintain(false) }
