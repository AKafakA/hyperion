package scheduler

import (
	"context"
	config "github.com/Vincent-lau/hyperion/internal/configs"
	"net"
	"os"
	"time"

	log "github.com/sirupsen/logrus"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"

	pb "github.com/Vincent-lau/hyperion/internal/message"
)

func (sched *Scheduler) AsClient() {
	ctrlAddr := findCtlAddr()

	sched.regWithCtl(ctrlAddr)
	sched.connectToPl(ctrlAddr)

	neighbours := sched.getNeighbours()
	sched.connectNeigh(neighbours)
	sched.waitForFinish()
}

func (sched *Scheduler) connectToPl(ctlAddr net.IP) {
	conn := sched.Conn(ctlAddr.String(),config.PlacementPort,
		grpc.WithTransportCredentials(insecure.NewCredentials()))

	sched.mu.Lock()
	sched.ctlPlStub = pb.NewJobPlacementClient(conn)
	sched.mu.Unlock()

}

func (sched *Scheduler) Conn(addr string, port string, opts ...grpc.DialOption) *grpc.ClientConn {
	conn, err := grpc.Dial(addr+":"+port, opts...)
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
			"for":   "placement",
		}).Fatal("Could not connect to controller")
	}
	return conn

}

func (sched *Scheduler) regWithCtl(ctlAddr net.IP) {
	sched.mu.Lock()
	defer sched.mu.Unlock()

	conn := sched.Conn(ctlAddr.String(),*config.CtlPort,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	sched.ctlRegStub = pb.NewSchedRegClient(conn)

	host, err := os.Hostname()
	myIP := getOutboundIP()

	if err != nil {
		log.Fatal("Could not get hostname")
	}

	sched.hostname = host

	var r *pb.RegReply
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*2)
		defer cancel()
		sched.mu.Unlock()
		r, err = sched.ctlRegStub.Reg(ctx, &pb.RegRequest{
			Name: host,
			Ip:   myIP.String(),
		})
		sched.mu.Lock()

		if err != nil {
			log.WithFields(log.Fields{
				"error": err,
			}).Debug("Could not register with controller")
			sched.mu.Unlock()
			time.Sleep(time.Second)
			sched.mu.Lock()
		} else {
			break
		}
	}

	sched.me = int(r.GetYou())
	log.WithFields(log.Fields{
		"me": sched.me,
	}).Debug("My number")

}

func (sched *Scheduler) getNeighbours() []string {
	sched.mu.Lock()
	defer sched.mu.Unlock()

	wt := 1
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		sched.mu.Unlock()
		r, err := sched.ctlRegStub.GetNeighbours(ctx, &pb.NeighboursRequest{
			Me: int32(sched.me),
		})
		sched.mu.Lock()
		if err != nil {
			log.WithFields(log.Fields{
				"error": err,
			}).Debug("could not get neighbours")

			sched.mu.Unlock()
			time.Sleep(time.Second * time.Duration(wt))
			wt *= 2
			sched.mu.Lock()
		} else {
			sched.expectedIn = int(r.GetInNeighbours())

			log.WithFields(log.Fields{
				"neighbours":  r.GetNeigh(),
				"expected in": r.GetInNeighbours(),
			}).Debug("got neighbours")
			return r.GetNeigh()
		}
	}
}

func (sched *Scheduler) connectNeigh(neighbours []string) {
	sched.mu.Lock()
	defer sched.mu.Unlock()

	for _, n := range neighbours {
		conn := sched.Conn(n,*config.SchedPort,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithKeepaliveParams(keepalive.ClientParameters{}))


		stub := pb.NewRatioConsensusClient(conn)

		var r *pb.HelloReply
		for {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second*2)
			defer cancel()
			sched.mu.Unlock()
			var err error
			r, err = stub.SayHello(ctx, &pb.HelloRequest{Me: int32(sched.me)})
			sched.mu.Lock()
			if err != nil {
				log.WithFields(log.Fields{
					"neighbour addr": n,
					"error":          err,
				}).Warn("could not greet")
				sched.mu.Unlock()
				time.Sleep(time.Second * 3)
				sched.mu.Lock()
			} else {
				log.WithFields(log.Fields{
					"to": r.GetMe(),
				}).Debug("greeted")
				break
			}
		}

		sched.outConns = append(sched.outConns, int(r.GetMe()))
		sched.stubs[int(r.GetMe())] = stub
	}
	sched.outNeighbours = len(sched.outConns)

	log.WithFields(log.Fields{
		"number of out neighbours": sched.outNeighbours,
	}).Debug("connected to all neighbours")

	// now wait for all neighbours to connect to me
	for sched.expectedIn != sched.inNeighbours {
		log.WithFields(log.Fields{
			"expected in":   sched.expectedIn,
			"in neighbours": sched.inNeighbours,
		}).Debug("waiting for all neighbours to connect to me")
		sched.neighCond.Wait()
	}

	log.Debug("all neighbours connected to me")

}

func (sched *Scheduler) waitForFinish() {
	sched.mu.Lock()
	defer sched.mu.Unlock()

	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		sched.mu.Unlock()
		r, err := sched.ctlRegStub.FinSetup(ctx, &pb.SetupRequest{
			Me:           int32(sched.me),
			InNeighbours: int64(sched.inNeighbours),
		})
		sched.mu.Lock()

		if err != nil {
			log.WithFields(log.Fields{
				"error":    err,
				"finished": r.GetFinished(),
			}).Warn("error sending finish to controller")
			sched.mu.Unlock()
			time.Sleep(time.Second)
			sched.mu.Lock()
		} else {
			break
		}
	}

	for !sched.setup {
		sched.startCond.Wait()
	}

	log.WithFields(log.Fields{
		"in nieghbours": sched.inNeighbours,
	}).Debug("received finish setup from controller, all schedulers are connected")

}

func getOutboundIP() net.IP {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)

	return localAddr.IP
}

func findCtlAddr() net.IP {
	for {
		ips, err := net.LookupIP(*config.CtlDNS)
		if err != nil {
			log.WithFields(log.Fields{
				"error": err,
			}).Debug("Could not get IPs")
			time.Sleep(time.Second * 5)
		} else {
			return ips[0]
		}
	}
}
