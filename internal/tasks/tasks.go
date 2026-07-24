package tasks

import (
	"errors"
	"sync"
)

type Task func() error

type TaskGroup struct {
	tasks []Task
}

func (tg *TaskGroup) Add(t Task) {
	tg.tasks = append(tg.tasks, t)
}

func (tg *TaskGroup) Run() error {
	if len(tg.tasks) == 0 {
		return nil
	}

	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		errs []error
	)

	for _, t := range tg.tasks {
		wg.Add(1)
		go func() {
			err := t()
			if err != nil {
				mu.Lock()
				defer mu.Unlock()

				errs = append(errs, err)
			}
		}()
	}

	wg.Wait()

	return errors.Join(errs...)
}
