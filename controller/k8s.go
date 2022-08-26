package controller

import (
	"context"
	"fmt"
	"time"

	log "github.com/sirupsen/logrus"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	schedulerName = "my-controller"
)

func MyNode(hostname string) (*v1.Node, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		panic(err.Error())
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}
	pods, err := clientset.CoreV1().Pods("dist-sched").List(context.TODO(), metav1.ListOptions{
		FieldSelector: fmt.Sprintf("metadata.name=%s", hostname),
	})
	if err != nil {
		panic(err.Error())
	}

	n, err := clientset.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s", "kubernetes.io/hostname", pods.Items[0].Spec.NodeName),
	})

	return &n.Items[0], err
}

func (ctl *Controller) findNodes() {
	// TODO add informer to get the list of nodes
	nodes, _ := ctl.clientset.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})
	nMap := make(map[string]*v1.Node)

	for _, node := range nodes.Items {
		nMap[node.Name] = node.DeepCopy()
	}

	ctl.nodeMap = nMap
	log.WithFields(log.Fields{
		"node map":        ctl.nodeMap,
		"number of nodes": len(nodes.Items),
	}).Debug("found nodes")
}

func (ctl *Controller) jobsFromPodQueue(podChan chan *v1.Pod) {
	for {
		// get pods in all the namespaces by omitting namespace
		// Or specify namespace to get pods in particular namespace

		pods, err := ctl.clientset.CoreV1().Pods("").List(context.TODO(), metav1.ListOptions{})

		if err != nil {
			log.WithFields(log.Fields{
				"error": err,
			}).Panic("error getting pods")
		}

		log.WithFields(log.Fields{
			"number of pods": len(pods.Items),
		}).Debug("There are pods in the cluster")

		watch, err := ctl.clientset.CoreV1().Pods("").Watch(context.TODO(), metav1.ListOptions{
			FieldSelector: fmt.Sprintf("spec.schedulerName=%s,spec.nodeName=", schedulerName),
		})
		if err != nil {
			panic(err.Error())
		}

		for event := range watch.ResultChan() {
			if event.Type != "ADDED" {
				continue
			}
			p := event.Object.(*v1.Pod)
			podChan <- p

			log.WithFields(log.Fields{
				"pod name":      p.Name,
				"pod namespace": p.Namespace,
			}).Debug("found a pod to schedule")
		}

	}
}

func getJobDemand(pod *v1.Pod) float64 {

	var tot int64 = 0
	for _, c := range pod.Spec.Containers {
		cpu := c.Resources.Requests.Cpu().MilliValue()
		tot += cpu
	}
	log.WithFields(log.Fields{
		"pod name": pod.Name,
		"cpu":      tot,
	}).Debug("total cpu requests for pod")

	return float64(tot)
}

func (ctl *Controller) placePodToNode(node *v1.Node, pod *v1.Pod) error {

	ctl.clientset.CoreV1().Pods(pod.Namespace).Bind(context.TODO(), &v1.Binding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pod.Name,
			Namespace: pod.Namespace,
		},
		Target: v1.ObjectReference{
			APIVersion: "v1",
			Kind:       "Node",
			Name:       node.Name,
		},
	}, metav1.CreateOptions{})

	log.WithFields(log.Fields{
		"pod name":  pod.Name,
		"node name": node.Name,
	}).Debug("binding pod to node")

	timestamp := time.Now().UTC()
	ctl.clientset.CoreV1().Events(pod.Namespace).Create(context.TODO(), &v1.Event{
		Count:          1,
		Message:        "binding pod to node",
		Reason:         "Scheduled",
		LastTimestamp:  metav1.NewTime(timestamp),
		FirstTimestamp: metav1.NewTime(timestamp),
		Type:           "Normal",
		Source: v1.EventSource{
			Component: schedulerName,
		},
		InvolvedObject: v1.ObjectReference{
			Kind:      "Pod",
			Name:      pod.Name,
			Namespace: pod.Namespace,
			UID:       pod.UID,
		},
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: pod.Name + "-",
		},
	}, metav1.CreateOptions{})

	return nil

}

// func (ctl *Controller) PlacePod() {
// 	// creates the in-cluster config
// 	config, err := rest.InClusterConfig()
// 	if err != nil {
// 		panic(err.Error())
// 	}
// 	// creates the clientset
// 	clientset, err := kubernetes.NewForConfig(config)
// 	if err != nil {
// 		panic(err.Error())
// 	}

// 	for {
// 		// get pods in all the namespaces by omitting namespace
// 		// Or specify namespace to get pods in particular namespace

// 		pods, err := clientset.CoreV1().Pods("").List(context.TODO(), metav1.ListOptions{})
// 		if err != nil {
// 			panic(err.Error())
// 		}
// 		log.WithFields(log.Fields{
// 			"number of pods": len(pods.Items),
// 		}).Debug("There are pods in the cluster")

// 		watch, err := clientset.CoreV1().Pods("").Watch(context.TODO(), metav1.ListOptions{
// 			FieldSelector: fmt.Sprintf("spec.schedulerName=%s,spec.nodeName=", schedulerName),
// 		})
// 		if err != nil {
// 			panic(err.Error())
// 		}

// 		for event := range watch.ResultChan() {
// 			if event.Type != "ADDED" {
// 				continue
// 			}
// 			p := event.Object.(*v1.Pod)
// 			log.WithFields(log.Fields{
// 				"pod name":      p.Name,
// 				"pod namespace": p.Namespace,
// 			}).Debug("found a pod to schedule")

// 			toBind, err := findNode(clientset)
// 			if err != nil {
// 				panic(err.Error())
// 			}

// 			clientset.CoreV1().Pods(p.Namespace).Bind(context.TODO(), &v1.Binding{
// 				ObjectMeta: metav1.ObjectMeta{
// 					Name:      p.Name,
// 					Namespace: p.Namespace,
// 				},
// 				Target: v1.ObjectReference{
// 					APIVersion: "v1",
// 					Kind:       "Node",
// 					Name:       toBind.Name,
// 				},
// 			}, metav1.CreateOptions{})

// 			log.WithFields(log.Fields{
// 				"pod name":  p.Name,
// 				"node name": toBind.Name,
// 			}).Debug("binding pod to node")

// 			timestamp := time.Now().UTC()
// 			clientset.CoreV1().Events(p.Namespace).Create(context.TODO(), &v1.Event{
// 				Count:          1,
// 				Message:        "binding pod to node",
// 				Reason:         "Scheduled",
// 				LastTimestamp:  metav1.NewTime(timestamp),
// 				FirstTimestamp: metav1.NewTime(timestamp),
// 				Type:           "Normal",
// 				Source: v1.EventSource{
// 					Component: schedulerName,
// 				},
// 				InvolvedObject: v1.ObjectReference{
// 					Kind:      "Pod",
// 					Name:      p.Name,
// 					Namespace: p.Namespace,
// 					UID:       p.UID,
// 				},
// 				ObjectMeta: metav1.ObjectMeta{
// 					GenerateName: p.Name + "-",
// 				},
// 			}, metav1.CreateOptions{})

// 		}
// 	}

// }
