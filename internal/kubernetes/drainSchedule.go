package kubernetes

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"go.opencensus.io/stats"
	"go.opencensus.io/tag"
	"go.uber.org/zap"
	core "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
)

const (
	SetConditionTimeout     = 10 * time.Second
	SetConditionRetryPeriod = 50 * time.Millisecond
)

type DrainScheduler interface {
	HasSchedule(name string) (has, failed bool)
	Schedule(node *v1.Node) (time.Time, error)
	DeleteSchedule(name string)
	IsScheduledByOldEvent(name string, transitionTime time.Time) bool
}

type DrainSchedules struct {
	sync.Mutex
	schedules map[string]*schedule

	lastDrainScheduledFor time.Time
	period                time.Duration

	logger        *zap.Logger
	drainer       Drainer
	eventRecorder record.EventRecorder
}

func NewDrainSchedules(drainer Drainer, eventRecorder record.EventRecorder, period time.Duration, logger *zap.Logger) DrainScheduler {
	return &DrainSchedules{
		schedules:     map[string]*schedule{},
		period:        period,
		logger:        logger,
		drainer:       drainer,
		eventRecorder: eventRecorder,
	}
}

func (d *DrainSchedules) IsScheduledByOldEvent(name string, transitionTime time.Time) bool {
	d.Lock()
	defer d.Unlock()
	sched, ok := d.schedules[name]
	if !ok {
		return false
	}
	return sched.when.Before(transitionTime) && !sched.isFailed() && !sched.finish.IsZero()
}

func (d *DrainSchedules) HasSchedule(name string) (has, failed bool) {
	d.Lock()
	defer d.Unlock()
	sched, ok := d.schedules[name]
	if !ok {
		return false, false
	}
	d.logger.Info("HasSchedule", zap.String("node", name), zap.Time("when", sched.when), zap.Time("finish", sched.finish), zap.Bool("isFailed", sched.isFailed()))
	return true, sched.isFailed()
}

func (d *DrainSchedules) DeleteSchedule(name string) {
	d.Lock()
	defer d.Unlock()
	if s, ok := d.schedules[name]; ok {
		s.timer.Stop()
		delete(d.schedules, name)
	} else {
		d.logger.Warn("Entry not found in deletion schedule", zap.String("node", name))
	}
}

func (d *DrainSchedules) WhenNextSchedule() time.Time {
	// compute drain schedule time
	sooner := time.Now().Add(SetConditionTimeout + time.Second)
	when := d.lastDrainScheduledFor.Add(d.period)
	if when.Before(sooner) {
		when = sooner
	}
	return when
}

func (d *DrainSchedules) Schedule(node *v1.Node) (time.Time, error) {
	d.Lock()
	if sched, ok := d.schedules[node.GetName()]; ok {
		d.Unlock()
		return sched.when, NewAlreadyScheduledError() // we already have a schedule planned
	}

	// compute drain schedule time
	when := d.WhenNextSchedule()
	d.lastDrainScheduledFor = when
	d.schedules[node.GetName()] = d.newSchedule(node, when)
	d.Unlock()

	// Mark the node with the condition stating that drain is scheduled
	if err := RetryWithTimeout(
		func() error {
			return d.drainer.MarkDrain(node, when, time.Time{}, false)
		},
		SetConditionRetryPeriod,
		SetConditionTimeout,
	); err != nil {
		// if we cannot mark the node, let's remove the schedule
		d.logger.Info("Delete Schedule")
		d.DeleteSchedule(node.GetName())
		return time.Time{}, err
	}
	return when, nil
}

type schedule struct {
	when   time.Time
	failed int32
	finish time.Time
	timer  *time.Timer
}

func (s *schedule) setFailed() {
	atomic.StoreInt32(&s.failed, 1)
}

func (s *schedule) isFailed() bool {
	return atomic.LoadInt32(&s.failed) == 1
}

func (d *DrainSchedules) newSchedule(node *v1.Node, when time.Time) *schedule {
	sched := &schedule{
		when: when,
	}
	sched.timer = time.AfterFunc(time.Until(when), func() {
		log := d.logger.With(zap.String("node", node.GetName()))
		nr := &core.ObjectReference{Kind: "Node", Name: node.GetName(), UID: types.UID(node.GetName())}
		tags, _ := tag.New(context.Background(), tag.Upsert(TagNodeName, node.GetName())) // nolint:gosec
		d.eventRecorder.Event(nr, core.EventTypeWarning, eventReasonDrainStarting, "Draining node")
		if err := d.drainer.Drain(node); err != nil {
			log.Info("Failed to drain", zap.Error(err))

			sched.finish = time.Now()
			sched.setFailed()
			tags, _ = tag.New(tags, tag.Upsert(TagResult, tagResultFailed)) // nolint:gosec
			stats.Record(tags, MeasureNodesDrained.M(1))
			d.eventRecorder.Eventf(nr, core.EventTypeWarning, eventReasonDrainFailed, "Draining failed: %v", err)
			if err := RetryWithTimeout(
				func() error {
					return d.drainer.MarkDrain(node, when, sched.finish, true)
				},
				SetConditionRetryPeriod,
				SetConditionTimeout,
			); err != nil {
				log.Error("Failed to place condition following drain failure")
			}
			return
		}

		log.Info("Drained")
		sched.finish = time.Now()
		tags, _ = tag.New(tags, tag.Upsert(TagResult, tagResultSucceeded)) // nolint:gosec
		stats.Record(tags, MeasureNodesDrained.M(1))
		d.eventRecorder.Event(nr, core.EventTypeWarning, eventReasonDrainSucceeded, "Drained node")
		if err := RetryWithTimeout(
			func() error {
				return d.drainer.MarkDrain(node, when, sched.finish, false)
			},
			SetConditionRetryPeriod,
			SetConditionTimeout,
		); err != nil {
			d.eventRecorder.Eventf(nr, core.EventTypeWarning, eventReasonDrainFailed, "Failed to place drain condition: %v", err)
			log.Error(fmt.Sprintf("Failed to place condition following drain success : %v", err))
		}
	})
	return sched
}

type AlreadyScheduledError struct {
	error
}

func NewAlreadyScheduledError() error {
	return &AlreadyScheduledError{
		fmt.Errorf("drain schedule is already planned for that node"),
	}
}
func IsAlreadyScheduledError(err error) bool {
	_, ok := err.(*AlreadyScheduledError)
	return ok
}
