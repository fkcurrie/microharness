package scheduler

import (
	"context"
	"log"
	"os/exec"
	"time"

	"microharness/pkg/config"
	"microharness/pkg/store"

	"github.com/robfig/cron/v3"
)

type Scheduler struct {
	cron  *cron.Cron
	store *store.Store
	jobs  []config.JobConfig
}

func New(jobs []config.JobConfig, dbStore *store.Store) *Scheduler {
	c := cron.New(cron.WithSeconds())
	return &Scheduler{
		cron:  c,
		store: dbStore,
		jobs:  jobs,
	}
}

func (s *Scheduler) Start() {
	for _, job := range s.jobs {
		if !job.Enabled {
			continue
		}

		j := job
		schedule := j.Schedule
		// Normalize simple ticker syntax for cron/v3
		if schedule == "@every 15m" {
			schedule = "0 */15 * * * *"
		} else if schedule == "@every 5m" {
			schedule = "0 */5 * * * *"
		} else if schedule == "@every 1m" {
			schedule = "0 */1 * * * *"
		}

		_, err := s.cron.AddFunc(schedule, func() {
			s.runJob(j)
		})
		if err != nil {
			log.Printf("[Scheduler] Error scheduling job %s: %v", j.Name, err)
		} else {
			log.Printf("[Scheduler] Scheduled job '%s' with schedule '%s'", j.Name, schedule)
		}
	}

	s.cron.Start()
}

func (s *Scheduler) RunJobNow(j config.JobConfig) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", j.Command)
	output, err := cmd.CombinedOutput()

	status := "SUCCESS"
	if err != nil {
		status = "FAILED"
	}

	if s.store != nil {
		_ = s.store.LogJob(j.Name, j.Target, status, string(output))
	}

	return string(output), err
}

func (s *Scheduler) runJob(j config.JobConfig) {
	out, err := s.RunJobNow(j)
	if err != nil {
		log.Printf("[Job %s] FAILED: %v | Output: %s", j.Name, err, out)
	} else {
		log.Printf("[Job %s] SUCCESS | Output: %s", j.Name, out)
	}
}

func (s *Scheduler) Stop() {
	s.cron.Stop()
}
