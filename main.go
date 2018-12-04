
package main

import (
	"flag"
	"io"
	"fmt"
	"strconv"
	"path/filepath"

	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	"k8s.io/client-go/util/retry"

	"log"
	"time"
	"golang.org/x/net/context"
	gogrpc "google.golang.org/grpc"
	pb "github.com/Ankr-network/dccn-rpc/protocol"
)

const (
        CREATE_TASK = "create a task"
        LIST_TASK = "list a task"
        DELETE_TASK = "delete a task"
        UPDATE_REPLICA = "update replica"
)

const (
        ADDRESS  = "10.0.0.61:50051"
        MAX_REPLICA = 20
)

var addressCLI = ""
var dcNameCLI = ""
var totalPodNum =  0


// runRouteChat receives a sequence of route notes, while sending notes for various locations.
func sendTaskStatus(client pb.DccncliClient, clientset *kubernetes.Clientset) int{
        var ret int = 0
        var taskType string
        ctx, cancel := context.WithTimeout(context.Background(), 1000*time.Second)
        defer cancel()
        stream, err := client.K8Task(ctx)
        if err != nil {
            //log.Fatalf("%v.RouteChat(_) = _, %v", client, err)
            return 3
        }
        waitc := make(chan struct{})
        go func() {
            for {
                in, err := stream.Recv()
                if err == io.EOF {
                        close(waitc)
                        time.Sleep(3 * time.Second)
                        continue
                }
                if err != nil {
                        fmt.Println("Failed to receive a note : %v", err)
                        ret = 1
                        return
                }

                if in.Type == "HeartBeat" {
                    fmt.Printf("Heartbeat!\n")
                } else  {
                    fmt.Printf("Got message %d %s %s %s %s\n", in.Taskid, in.Name, in.Image, in.Extra, in.Type)

                    if in.Type == "NewTask" {
                        taskType = CREATE_TASK
                    } else if in.Type == "CancelTask" {
                        taskType = DELETE_TASK
                    } else if in.Type == "UpdateReplica" {
                        taskType = UPDATE_REPLICA
                    } else {
                        fmt.Println("Unknown task type:", in.Type)
                    }
                }

                if taskType == CREATE_TASK {
                    fmt.Printf("starting the task\n")
                    ret := ankr_create_task(clientset, in.Name, in.Image)
                    if ret {
                        podNumNew := 0
                        podsClient, err := clientset.CoreV1().Pods(apiv1.NamespaceDefault).List(metav1.ListOptions{})
                        if err != nil {
                            return
                        }

                        for _, pod := range podsClient.Items {
                            if pod.Status.Phase != "Running" && !pod.Status.ContainerStatuses[0].Ready  {
                                fmt.Println(pod.Name, " not running.")
                                continue
                            }
                            podNumNew += 1
                        }

                        fmt.Printf("total tasks %d; after creating total tasks %d\n", totalPodNum, podNumNew)
                        if  totalPodNum >= podNumNew {
                            fmt.Println("remove the failed task.")
                            ankr_delete_task(clientset, in.Name)
                            return
                        } else {
                            totalPodNum = podNumNew
                        }

                        fmt.Printf("finish starting the task\n")
                        var messageSucc = pb.K8SMessage{Taskid: in.Taskid, Taskname:in.Name, Status:"StartSuccess", Datacenter:dcNameCLI}
                        if err := stream.Send(&messageSucc); err != nil {
                            fmt.Printf("Failed to send a note: %v\n", err)
                        }

                    } else {
                        fmt.Printf("fail to start the task\n")
                        var messageSucc = pb.K8SMessage{Taskid: in.Taskid, Taskname:in.Name, Status:"StartFailure", Datacenter:dcNameCLI}
                        if err := stream.Send(&messageSucc); err != nil {
                            fmt.Printf("Failed to send a note: %v\n", err)
                        }
                    }
                }

                if taskType == DELETE_TASK {
                    fmt.Printf("canceling the task")
                    ret := ankr_delete_task(clientset, in.Name)
                    if !ret {
                        fmt.Printf("fail to cancel the task")
                        var messageSucc = pb.K8SMessage{Taskid: in.Taskid, Taskname:in.Name, Status:"CancelFailure", Datacenter:dcNameCLI}
                        if err := stream.Send(&messageSucc); err != nil {
                            fmt.Printf("Failed to send a note: %v\n", err)
                        }
                    } else {
                        fmt.Printf("finish canceling the task")
                        var messageSucc = pb.K8SMessage{Taskid: in.Taskid, Taskname:in.Name, Status:"Cancelled", Datacenter:dcNameCLI}
                        if err := stream.Send(&messageSucc); err != nil {
                            fmt.Printf("Failed to send a note: %v\n", err)
                        }
                    }
                }

                if taskType == UPDATE_REPLICA {
                    fmt.Printf("updating the replica")
                }

                taskType = ""
            }
        }()

        //var messageFail = pb.TaskStatus{Taskid: -1, Status:"Failure"}
        for {
            var messageSucc = pb.K8SMessage{Datacenter:dcNameCLI, Taskname:"", Type:"HeartBeat", Report:ankr_list_task(clientset)}
            if err := stream.Send(&messageSucc); err != nil {
                fmt.Println("Failed to send a note: %v", err)
                ret = 2
                return ret
            } else {
                fmt.Printf("Send message to Hub, %s \n", messageSucc.Type)
            }

            time.Sleep(30 * time.Second)
        }

        //stream.CloseSend()
        <-waitc

        return  0
}


func querytask(clientset *kubernetes.Clientset) int{
        var hubAddress string = addressCLI
        if len(hubAddress) == 0 {
            hubAddress = ADDRESS
        }

	conn, err := gogrpc.Dial(hubAddress, gogrpc.WithInsecure())
	if err != nil {
	    //log.Fatalf("did not connect: %v", err)
            return 1
	}
	defer conn.Close()
	c := pb.NewDccncliClient(conn)

        return sendTaskStatus(c, clientset)
/*synchronous one time call*/
/*
	ctx, cancel := context.WithTimeout(context.Background(), 30 * time.Second )
	defer cancel()

        r, err := c.K8QueryTask(ctx, &pb.QueryTaskRequest{Name:"datacenter_2"})
        if err != nil {
            fmt.Printf("Fail to connect to server. Error:\n") 
            log.Fatalf("Client: could not send: %v", err)
        }

        fmt.Printf("received new task  : %d %s %s \n", r.Taskid, r.Name, r.Extra)
*/
}

func sendreport() {
        var hubAddress string = addressCLI
        if len(hubAddress) == 0 {
            hubAddress = ADDRESS
        }

	conn, err := gogrpc.Dial(hubAddress, gogrpc.WithInsecure())
	if err != nil {
	    log.Fatalf("did not connect: %v", err)
	}
	defer conn.Close()
	c := pb.NewDccncliClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 30 * time.Second )
	defer cancel()
	r, err := c.K8ReportStatus(ctx, &pb.ReportRequest{Name:dcNameCLI,Report:"job2 job2 job3 host 100", Host:"127.0.0.67", Port:5009 })
	if err != nil {
            fmt.Printf("Fail to connect to server. Error:\n") 
	    log.Fatalf("Client: could not send: %v", err)
	}

	fmt.Printf("received Status : %s \n", r.Status)
}

func ankr_delete_task(clientset *kubernetes.Clientset, dockerName string) bool {
	deploymentsClient := clientset.AppsV1().Deployments(apiv1.NamespaceDefault)

	fmt.Println("Deleting deployment...")
	deletePolicy := metav1.DeletePropagationForeground
	if err := deploymentsClient.Delete(dockerName, &metav1.DeleteOptions{
		PropagationPolicy: &deletePolicy,
	}); err != nil {
		//panic(err)
                return false
	}

        return true
}


func ankr_update_task(clientset *kubernetes.Clientset, num int32) bool {
        deploymentsClient := clientset.AppsV1().Deployments(apiv1.NamespaceDefault)
        retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// RetryOnConflict uses exponential backoff to avoid exhausting the apiserver
		result, getErr := deploymentsClient.Get("demo-deployment", metav1.GetOptions{})
		if getErr != nil {
			panic(fmt.Errorf("Failed to get latest version of Deployment: %v", getErr))
		}

		result.Spec.Replicas = int32Ptr(num)
		//result.Spec.Template.Spec.Containers[0].Image = "nginx:1.13" // change nginx version
		_, updateErr := deploymentsClient.Update(result)
		return updateErr
	})
	if retryErr != nil {
		panic(fmt.Errorf("Update failed: %v", retryErr))
	}

        return true
}

func ankr_list_task(clientset *kubernetes.Clientset) string {
        result  := ""
	deploymentsClient := clientset.AppsV1().Deployments(apiv1.NamespaceDefault)
        podsClient, err := clientset.CoreV1().Pods(apiv1.NamespaceDefault).List(metav1.ListOptions{})
        if err != nil {
            return ""
        }

        for _, pod := range podsClient.Items {
            if pod.Status.Phase != "Running" && !pod.Status.ContainerStatuses[0].Ready  {
                fmt.Println(pod.Name, " not running.")
            }
	    //fmt.Println(pod.Name, ":", pod.Status.PodIP, ":", pod.Status.Phase, ":", pod.Status.Conditions,
                 //":", pod.Status.Message, ":", pod.Status.Reason, ":", pod.Status.HostIP, ":", pod.Status.StartTime, 
                 //":", pod.Status.InitContainerStatuses, ":", pod.Status.ContainerStatuses[0].Ready)
        }

	//fmt.Printf("Listing deployments in namespace %q:\n", apiv1.NamespaceDefault)
	list, err := deploymentsClient.List(metav1.ListOptions{})
	if err != nil {
                fmt.Printf("Probabaly the kubenetes(minikube) not started.\n")
		panic(err)
	}

	for _, d := range list.Items {
                _, err := deploymentsClient.Get(d.Name, metav1.GetOptions{})
                if err == nil {
                    //fmt.Printf("status.AvailableReplicas:%s\n", d.Status.AvailableReplicas)
                    //fmt.Printf("revision:%s\n", d.Revision)
                    //fmt.Printf("image:%s\n", d.Spec.Template.Spec.Containers[0].Image)
                    //fmt.Printf("NodeName:%s\n", d.Spec.Template.Spec.NodeName)
                    //fmt.Printf("Hostname:%s\n", d.Spec.Template.Spec.Hostname)
                    //fmt.Printf("containers:%s\n", d.Spec.Template.Spec.Containers[0])
                    //fmt.Printf("%s\n", cc)
                }
		fmt.Printf("task name: %s, image:%s (%d replicas running)\n\n", d.Name, 
                        d.Spec.Template.Spec.Containers[0].Image, *d.Spec.Replicas)
                result += "Task:" + string(d.Name) + "," + "Image:" + d.Spec.Template.Spec.Containers[0].Image + 
                        "Replicas:" + strconv.Itoa(int(*d.Spec.Replicas)) + "\n"
	}

        return result
}

func ankr_create_task(clientset *kubernetes.Clientset, dockerName string, dockerImage string) bool {
	deploymentsClient := clientset.AppsV1().Deployments(apiv1.NamespaceDefault)

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: string (
                              dockerName,
                        ),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(1),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": "ankr",
				},
			},
			Template: apiv1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": "ankr",
					},
				},
				Spec: apiv1.PodSpec{
					Containers: []apiv1.Container{
						{
							Name:  string (
                                                               dockerName,
                                                        ),
							Image: string (
                                                               dockerImage,
                                                        ),
							Ports: []apiv1.ContainerPort{
								{
									Name:          "http",
									Protocol:      apiv1.ProtocolTCP,
									ContainerPort: 80,
								},
							},
						},
					},
				},
			},
		},
	}

	fmt.Println("Creating deployment...")

        podsClient, err := clientset.CoreV1().Pods(apiv1.NamespaceDefault).List(metav1.ListOptions{})
        if err != nil {
            return false
        }

        totalPodNum = 0
        for _, pod := range podsClient.Items {
            totalPodNum += 1
            fmt.Println(pod.Name, pod.Status.PodIP)
        }

	result, err := deploymentsClient.Create(deployment)
	if err != nil {
                fmt.Println("probably already exist:.\n", err, result)
                return false
	}

        time.Sleep(2 * time.Second)

        return true
}

func int32Ptr(i int32) *int32 { return &i }

func main() {
        var taskType string
        var ipCLI string
        var portCLI string
        pboolCreate := flag.Bool("create", false, "create task")
        pboolList := flag.Bool("list", false, "list task")
        pboolDelete := flag.Bool("delete", false, "delete task")

        flag.StringVar(&ipCLI, "ip", "", "ankr hub ip address")
        flag.StringVar(&portCLI, "port", "", "ankr hub port number")
        flag.StringVar(&dcNameCLI, "dcName", "", "data center name")
        updateNumPtr := flag.Int("update", 0, "replica number")

	var kubeconfig *string
	if home := homedir.HomeDir(); home != "" {
		kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), 
                             "(optional) absolute path to the kubeconfig file")
	} else {
		kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	}

	flag.Parse()

        if len(dcNameCLI) == 0 {
            dcNameCLI = "datacenter_2"
        }
        if len(ipCLI) != 0 && len(portCLI) != 0 {
            // TODO: verify ip and port input
            addressCLI = ipCLI + ":" + portCLI
            fmt.Println(addressCLI)
        }

        if *pboolCreate {
            taskType = CREATE_TASK
        } else if *pboolList {
            taskType = LIST_TASK
        } else if *pboolDelete {
            taskType = DELETE_TASK
        } else if *updateNumPtr != 0 {
            taskType = UPDATE_REPLICA
        }

        if *updateNumPtr < 0 {
            fmt.Printf("invalid replica number:%d\n", *updateNumPtr)
            return
        } else if *updateNumPtr > MAX_REPLICA {
            fmt.Printf("replica number %d it too big. Maximum is %d.\n", *updateNumPtr, MAX_REPLICA)
            return
        } 

        fmt.Println(taskType)

	config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		panic(err)
	}

        fmt.Println(config.Host)

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err)
	}

        switch taskType {
            case CREATE_TASK:
                ankr_create_task(clientset, "demo-deployment", "nginx:1.12")
                return
            case LIST_TASK:
                fmt.Printf("%s", ankr_list_task(clientset))
                return
            case DELETE_TASK:
                ankr_delete_task(clientset, "demo-deployment")
                return
            case UPDATE_REPLICA:
                fmt.Printf("update to %d replica\n", *updateNumPtr)
                ankr_update_task(clientset, int32(*updateNumPtr))
                return
        }

        for { 
            ret := querytask(clientset)
            if ret != 0 {
                time.Sleep(3 * time.Second)
                fmt.Println("Reconnect.")
                continue
            } else {
                fmt.Println("Bye.")
                break
            }
        }
}
