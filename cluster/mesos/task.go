package mesos

import (
	"errors"
	"fmt"

	log "github.com/Sirupsen/logrus"
	"github.com/docker/docker/pkg/stringid"
	"github.com/docker/swarm/cluster"
	"github.com/gogo/protobuf/proto"
	"github.com/mesos/mesos-go/mesosproto"
	"github.com/mesos/mesos-go/mesosutil"
)

type task struct {
	mesosproto.TaskInfo

	cluster *Cluster

	updates chan *mesosproto.TaskStatus

	config    *cluster.ContainerConfig
	error     chan error
	container chan *cluster.Container
}

func (t *task) ID() string {
	return t.TaskId.GetValue()
}

func (t *task) Do() bool {
	return t.cluster.scheduleTask(t)
}

func (t *task) build(slaveID string) {
	t.Command = &mesosproto.CommandInfo{Shell: proto.Bool(false)}

	t.Container = &mesosproto.ContainerInfo{
		Type: mesosproto.ContainerInfo_DOCKER.Enum(),
		Docker: &mesosproto.ContainerInfo_DockerInfo{
			Image: &t.config.Image,
		},
	}

	switch t.config.HostConfig.NetworkMode {
	case "none":
		t.Container.Docker.Network = mesosproto.ContainerInfo_DockerInfo_NONE.Enum()
	case "host":
		t.Container.Docker.Network = mesosproto.ContainerInfo_DockerInfo_HOST.Enum()
	case "bridge", "":
		// TODO handle -p and -P here
		t.Container.Docker.Network = mesosproto.ContainerInfo_DockerInfo_BRIDGE.Enum()
	default:
		log.Errorf("Unsupported network mode %q", t.config.HostConfig.NetworkMode)
		t.Container.Docker.Network = mesosproto.ContainerInfo_DockerInfo_BRIDGE.Enum()
	}

	if cpus := t.config.CpuShares; cpus > 0 {
		t.Resources = append(t.Resources, mesosutil.NewScalarResource("cpus", float64(cpus)))
	}

	if mem := t.config.Memory; mem > 0 {
		t.Resources = append(t.Resources, mesosutil.NewScalarResource("mem", float64(mem/1024/1024)))
	}

	if len(t.config.Cmd) > 0 && t.config.Cmd[0] != "" {
		t.Command.Value = &t.config.Cmd[0]
	}

	if len(t.config.Cmd) > 1 {
		t.Command.Arguments = t.config.Cmd[1:]
	}

	for key, value := range t.config.Labels {
		t.Container.Docker.Parameters = append(t.Container.Docker.Parameters, &mesosproto.Parameter{Key: proto.String("label"), Value: proto.String(fmt.Sprintf("%s=%s", key, value))})
	}

	t.SlaveId = &mesosproto.SlaveID{Value: &slaveID}
}

func newTask(c *Cluster, config *cluster.ContainerConfig, name string) (*task, error) {
	// save the name in labels as the mesos containerizer will override it
	config.SetNamespacedLabel("mesos.name", name)

	task := task{
		updates: make(chan *mesosproto.TaskStatus),

		cluster:   c,
		config:    config,
		error:     make(chan error),
		container: make(chan *cluster.Container),
	}

	ID := stringid.TruncateID(stringid.GenerateRandomID())
	if name != "" {
		ID = name + "." + ID
	}

	task.Name = &name
	task.TaskId = &mesosproto.TaskID{Value: &ID}

	return &task, nil
}

func (t *task) sendStatus(status *mesosproto.TaskStatus) {
	t.updates <- status
}

func (t *task) getStatus() *mesosproto.TaskStatus {
	return <-t.updates
}

func (t *task) monitor() (bool, error) {
	taskStatus := t.getStatus()

	switch taskStatus.GetState() {
	case mesosproto.TaskState_TASK_STAGING:
	case mesosproto.TaskState_TASK_STARTING:
	case mesosproto.TaskState_TASK_RUNNING:
	case mesosproto.TaskState_TASK_FINISHED:
		return true, nil
	case mesosproto.TaskState_TASK_FAILED:
		return true, errors.New(taskStatus.GetMessage())
	case mesosproto.TaskState_TASK_KILLED:
		return true, nil
	case mesosproto.TaskState_TASK_LOST:
		return true, errors.New(taskStatus.GetMessage())
	case mesosproto.TaskState_TASK_ERROR:
		return true, errors.New(taskStatus.GetMessage())
	}

	return false, nil
}