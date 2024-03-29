package controller

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	config "github.com/Vincent-lau/hyperion/internal/configs"
	pb "github.com/Vincent-lau/hyperion/internal/message"
	"github.com/Vincent-lau/hyperion/internal/metrics"
	"github.com/Vincent-lau/hyperion/internal/util"

	log "github.com/sirupsen/logrus"
)

func (ctl *Controller) Placement() {
	ctl.populateQueue()
	plStart = time.Now()

	go ctl.bcastPl()
	go ctl.waitForPl()
}

func (ctl *Controller) GetJob(ctx context.Context, in *pb.JobRequest) (*pb.JobReply, error) {
	if ctl.trial != int(in.GetTrial()) {
		log.WithFields(log.Fields{
			"sched trial": in.GetTrial(),
			"ctl trial":   ctl.trial,
		}).Debug("wrong trial")
		return nil, errors.New("wrong trial")
	}
	t := time.Now()

	if in.GetSize() < 0 {
		v := atomic.AddInt32(&ctl.fetched, 1)

		if atomic.AddInt32(&v, int32(-*config.NumSchedulers)) == 0 {
			if config.RandomPlaceWhenNoSpace {
				ctl.randPlace()
			} else {
				// signal to the goroutine there is no need to wait for pending jobs
				// to be placed
				ctl.placed <- -1
			}
		}
		return &pb.JobReply{}, nil
	}

	log.WithFields(log.Fields{
		"from":        in.GetNode(),
		"smallQueue":  ctl.jobQueue[2].Len(),
		"mediumQueue": ctl.jobQueue[1].Len(),
		"largeQueue":  ctl.jobQueue[0].Len(),
	}).Debug("got request, current queue status")

	j := ctl.heuPlace(config.Heuristic, in)

	ctl.tq.Put(time.Since(t).Microseconds())

	if j.Size() > 0 {
		go ctl.placePodToNode(in.GetNode(), j.Id())
	}

	// 	log.WithFields(log.Fields{
	// 		"requested size": in.GetSize(),
	// 		"smallQueue":     ctl.jobQueue[2],
	// 		"mediumQueue":    ctl.jobQueue[1],
	// 		"largeQueue":     ctl.jobQueue[0],
	// 	}).Debug("no job found, ask scheduler to stop")

	// TODO here we signal no more jobs when the head of the queue cannot satisfy

	return &pb.JobReply{Size: j.Size()}, nil

}

func (ctl *Controller) bcastPl() {
	log.Debug("broadcasting placement start")

	for _, s := range ctl.schedulers {
		go func(s string) {
			util.RetryRPC(&pb.EmptyRequest{}, ctl.schedStub[s].StartPlace)
		}(s)
	}
}

func getQueueIdx(size float64, mean float64, std float64) int {
	small := mean - std  // x < mu - std
	medium := mean + std // mu-std < x < mu + std

	if size >= medium {
		return 0
	} else if size >= small && size < medium {
		return 1
	} else if size < small {
		return 2
	} else {
		panic("invalid size")
	}
}

// put jobs into multiple queues
func (ctl *Controller) populateQueue() {
	for id, s := range ctl.jobDemand {
		i := getQueueIdx(s, ctl.dmdMean, ctl.dmdStd)
		if err := ctl.jobQueue[i].Put(&Job{id: id, size: s}); err != nil {
			log.WithFields(log.Fields{
				"error": err,
			}).Error("failed to put job into queue")
		}
	}

	PlLogger.WithFields(log.Fields{
		"smallQueue":  *ctl.jobQueue[2],
		"mediumQueue": *ctl.jobQueue[1],
		"largeQueue":  *ctl.jobQueue[0],
	}).Debug("populated queue")

}

func (ctl *Controller) waitForPl() {

	numJobs := len(ctl.jobDemand)
	for i := 0; i < numJobs; i++ {
		v := <-ctl.placed
		if v == -1 {
			log.Debug("some jobs cannot be placed, no need to wait for more jobs")
			break
		}
	}

	log.Debug("no more jobs can be placed")
	ctl.finPl()

}

func (ctl *Controller) finPl() {
	metrics.PlacementLatency.Observe(time.Since(plStart).Seconds())
	PlLogger.WithFields(log.Fields{
		"time taken": time.Since(plStart).Microseconds(),
	}).Info("placement time")

	elementsLeft := make([]float64, 0)
	jobSched := make([]float64, 0)
	s := make(map[int]bool)

	for _, q := range ctl.jobQueue {
		vs := q.Dispose()
		for _, v := range vs {
			elementsLeft = append(elementsLeft, v.(*Job).Size())
			s[v.(*Job).Id()] = true
		}
	}

	for i, j := range ctl.jobDemand {
		if _, ok := s[i]; ok && s[i] {
			s[i] = false
		} else {
			jobSched = append(jobSched, j)
		}
	}

	PlLogger.WithFields(log.Fields{
		"left elements":       elementsLeft,
		"scheduled elements":  jobSched,
		"each job fetch time": ctl.tq.Dispose(),
	}).Info("all jobs fetched, queue elements left")

	go ctl.newTrial()
}

// Randomly place the the rest of the jobs in the queue to nodes
func (ctl *Controller) randPlace() {

	log.WithFields(log.Fields{
		"smallQueue":  ctl.jobQueue[2].Len(),
		"mediumQueue": ctl.jobQueue[1].Len(),
		"largeQueue":  ctl.jobQueue[0].Len(),
	}).Debug("random placement")

	ns := make([]string, 0)
	for k := range ctl.nodeMap {
		ns = append(ns, k)
	}

	log.WithFields(log.Fields{
		"nodes": ns,
	}).Debug("got nodes")

	for _, q := range ctl.jobQueue {
		vs := q.Dispose()
		for _, v := range vs {
			j := v.(*Job).Id()
			// ctl.mu.Lock()
			// pod := ctl.jobPod[j]
			// ctl.mu.Unlock()

			// ri := rand.Intn(*config.NumSchedulers)
			log.WithFields(log.Fields{
				// "node": ns[ri],
				"job": j,
				// "name": pod.Name,
			}).Debug("randomly placed job")

			go ctl.placePodToNode(ns[0], v.(*Job).Id())
		}
	}
}
