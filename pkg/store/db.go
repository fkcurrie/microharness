package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

type JobLog struct {
	ID         int64     `json:"id"`
	JobName    string    `json:"job_name"`
	Target     string    `json:"target"`
	Status     string    `json:"status"` // "SUCCESS", "FAILED"
	Output     string    `json:"output"`
	ExecutedAt time.Time `json:"executed_at"`
}

type ChatMessage struct {
	ID        int64     `json:"id"`
	Role      string    `json:"role"` // "user", "assistant", "system"
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

func Open(dbPath string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}

	s := &Store{db: db}
	if err := s.initSchema(); err != nil {
		db.Close()
		return nil, err
	}

	return s, nil
}

func (s *Store) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS job_logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		job_name TEXT NOT NULL,
		target TEXT NOT NULL,
		status TEXT NOT NULL,
		output TEXT,
		executed_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS chat_messages (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		role TEXT NOT NULL,
		content TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	`
	_, err := s.db.Exec(schema)
	return err
}

func (s *Store) LogJob(jobName, target, status, output string) error {
	_, err := s.db.Exec("INSERT INTO job_logs (job_name, target, status, output) VALUES (?, ?, ?, ?)", jobName, target, status, output)
	return err
}

func (s *Store) SaveMessage(role, content string) error {
	_, err := s.db.Exec("INSERT INTO chat_messages (role, content) VALUES (?, ?)", role, content)
	return err
}

func (s *Store) GetRecentJobLogs(limit int) ([]JobLog, error) {
	rows, err := s.db.Query("SELECT id, job_name, target, status, output, executed_at FROM job_logs ORDER BY id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []JobLog
	for rows.Next() {
		var l JobLog
		if err := rows.Scan(&l.ID, &l.JobName, &l.Target, &l.Status, &l.Output, &l.ExecutedAt); err != nil {
			return nil, err
		}
		logs = append(logs, l)
	}
	return logs, nil
}

func (s *Store) GetRecentMessages(limit int) ([]ChatMessage, error) {
	rows, err := s.db.Query("SELECT id, role, content, created_at FROM chat_messages ORDER BY id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []ChatMessage
	for rows.Next() {
		var m ChatMessage
		if err := rows.Scan(&m.ID, &m.Role, &m.Content, &m.CreatedAt); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}

	// Reverse to return in chronological order
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}

	return msgs, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}
