// +build !windows

package libcontainerd // import "github.com/docker/docker/libcontainerd"

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/containerd/containerd"
	"github.com/containerd/containerd/services/server"
	"github.com/docker/docker/pkg/system"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

const (
	maxConnectionRetryCount = 3
	healthCheckTimeout      = 3 * time.Second
	shutdownTimeout         = 15 * time.Second
	configFile              = "containerd.toml"
	binaryName              = "docker-containerd"
	pidFile                 = "docker-containerd.pid"
)

type pluginConfigs struct {
	Plugins map[string]interface{} `toml:"plugins"`
}

type remote struct {
	sync.RWMutex
	server.Config

	daemonPid int
	logger    *logrus.Entry

	daemonWaitCh    chan struct{}
	clients         []*client
	shutdownContext context.Context
	shutdownCancel  context.CancelFunc
	shutdown        bool

	// Options
	startDaemon bool
	rootDir     string
	stateDir    string
	snapshotter string
	pluginConfs pluginConfigs
}

// New creates a fresh instance of libcontainerd remote.
/**
* 	1. 创建一个libcontained对象
		1. libcontainerd daemon 的root 目录为dockerroot/containerd/daemon
		2. libcontainerd daemon 的state 目录为dockerexecroot/containerd/daemon
*	2. 创建libcontainerd 的state 目录
	3. 创建一个libcontainerd client
	4. 启动一个goroutiner 来使用步骤3的client 来监控libcontainerd server 的状态
*/
func New(rootDir, stateDir string, options ...RemoteOption) (rem Remote, err error) {
	defer func() {
		if err != nil {
			err = errors.Wrap(err, "Failed to connect to containerd")
		}
	}()
	// 创建一个containerd daemon 的对象
	r := &remote{
		rootDir:  rootDir,
		stateDir: stateDir,
		Config: server.Config{
			Root:  filepath.Join(rootDir, "daemon"),  // 对应的是dockerroot/containerd/daemon
			State: filepath.Join(stateDir, "daemon"), // 对应的是dockerexecroot/containerd/daemon
		},
		pluginConfs: pluginConfigs{make(map[string]interface{})},
		daemonPid:   -1,
		logger:      logrus.WithField("module", "libcontainerd"),
	}
	r.shutdownContext, r.shutdownCancel = context.WithCancel(context.Background())

	rem = r
	for _, option := range options {
		if err = option.Apply(r); err != nil {
			return
		}
	}
	r.setDefaults()
	// 创建dockerexecroot/containerd目录，并且更改权限
	if err = system.MkdirAll(stateDir, 0700, ""); err != nil {
		return
	}

	if r.startDaemon {
		os.Remove(r.GRPC.Address)
		if err = r.startContainerd(); err != nil {
			return
		}
		defer func() {
			if err != nil {
				r.Cleanup()
			}
		}()
	}

	// This connection is just used to monitor the connection
	// 创建一个连接到r.GRPC.Address的containerd instance 的一个client
	client, err := containerd.New(r.GRPC.Address)
	if err != nil {
		return
	}
	if _, err := client.Version(context.Background()); err != nil {
		system.KillProcess(r.daemonPid)
		return nil, errors.Wrapf(err, "unable to get containerd version")
	}

	go r.monitorConnection(client)

	return r, nil
}

func (r *remote) NewClient(ns string, b Backend) (Client, error) {
	c := &client{
		stateDir:   r.stateDir,
		logger:     r.logger.WithField("namespace", ns),
		namespace:  ns,
		backend:    b,
		containers: make(map[string]*container),
	}

	rclient, err := containerd.New(r.GRPC.Address, containerd.WithDefaultNamespace(ns))
	if err != nil {
		return nil, err
	}
	c.remote = rclient

	go c.processEventStream(r.shutdownContext)

	r.Lock()
	r.clients = append(r.clients, c)
	r.Unlock()
	return c, nil
}

func (r *remote) Cleanup() {
	if r.daemonPid != -1 {
		r.shutdownCancel()
		r.stopDaemon()
	}

	// cleanup some files
	os.Remove(filepath.Join(r.stateDir, pidFile))

	r.platformCleanup()
}

func (r *remote) getContainerdPid() (int, error) {
	pidFile := filepath.Join(r.stateDir, pidFile)
	f, err := os.OpenFile(pidFile, os.O_RDWR, 0600)
	if err != nil {
		if os.IsNotExist(err) {
			return -1, nil
		}
		return -1, err
	}
	defer f.Close()

	b := make([]byte, 8)
	n, err := f.Read(b)
	if err != nil && err != io.EOF {
		return -1, err
	}

	if n > 0 {
		pid, err := strconv.ParseUint(string(b[:n]), 10, 64)
		if err != nil {
			return -1, err
		}
		if system.IsProcessAlive(int(pid)) {
			return int(pid), nil
		}
	}

	return -1, nil
}

// 读取containerexecroot/libcontainerd/daemon/containerd.toml 文件中的配置文件
// 默认是在/var/run/docker/libcontainerd/daemon 下面
func (r *remote) getContainerdConfig() (string, error) {
	path := filepath.Join(r.stateDir, configFile)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return "", errors.Wrapf(err, "failed to open containerd config file at %s", path)
	}
	defer f.Close()

	enc := toml.NewEncoder(f)
	if err = enc.Encode(r.Config); err != nil {
		return "", errors.Wrapf(err, "failed to encode general config")
	}
	if err = enc.Encode(r.pluginConfs); err != nil {
		return "", errors.Wrapf(err, "failed to encode plugin configs")
	}

	return path, nil
}

/* 启动docker-conatinerd 进程
1. 读取/var/run/docker/containerd 目录下的docker-containerd.pid 文件。读取pid
2. 如果pid 不为零，则直接返回
3. 读取/var/run/docker/conatined 下的配置文件 containerd.toml 文件
4. 将docker-containerd --config containerd.toml 运行起来
*/
func (r *remote) startContainerd() error {
	pid, err := r.getContainerdPid()
	if err != nil {
		return err
	}

	// containerd daemon 已经启动
	if pid != -1 {
		r.daemonPid = pid
		logrus.WithField("pid", pid).
			Infof("libcontainerd: %s is still running", binaryName)
		return nil
	}
	// 读取config文件中的内容
	configFile, err := r.getContainerdConfig()
	if err != nil {
		return err
	}

	args := []string{"--config", configFile}
	// 将docker-containerd --config 运行起来
	cmd := exec.Command(binaryName, args...)
	// redirect containerd logs to docker logs
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = containerdSysProcAttr()
	// clear the NOTIFY_SOCKET from the env when starting containerd
	cmd.Env = nil
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "NOTIFY_SOCKET") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	r.daemonWaitCh = make(chan struct{})
	go func() {
		// Reap our child when needed
		if err := cmd.Wait(); err != nil {
			r.logger.WithError(err).Errorf("containerd did not exit successfully")
		}
		close(r.daemonWaitCh)
	}()

	r.daemonPid = cmd.Process.Pid
	// 写docker-containerd.pid 文件
	err = ioutil.WriteFile(filepath.Join(r.stateDir, pidFile), []byte(fmt.Sprintf("%d", r.daemonPid)), 0660)
	if err != nil {
		system.KillProcess(r.daemonPid)
		return errors.Wrap(err, "libcontainerd: failed to save daemon pid to disk")
	}

	logrus.WithField("pid", r.daemonPid).
		Infof("libcontainerd: started new %s process", binaryName)

	return nil
}

/*
* 监控containerd daemon
* 也就是通过一个containerd client 来不断的进行连接监控
* 1. 如果连接成功说明 监控成功，等待下次监测
* 2. 如果连连接失败
		1. 判断daemon 是否已经初始化并启动，如果没有则继续等待
		2. 如果已经启动
			1. 如果监测次数小于最大次数并且daemon 进程或者，则继续等下下次监测
			2. 否则，则重置监测次数为0
				1. 如果damon 依旧是active 状态，则强转重启daemon
				2. 重启启动containerd
*/
func (r *remote) monitorConnection(monitor *containerd.Client) {
	var transientFailureCount = 0

	for {
		select {
		case <-r.shutdownContext.Done():
			r.logger.Info("stopping healthcheck following graceful shutdown")
			monitor.Close()
			return
		case <-time.After(500 * time.Millisecond):
		}

		ctx, cancel := context.WithTimeout(r.shutdownContext, healthCheckTimeout)
		_, err := monitor.IsServing(ctx)
		cancel()
		// 如果可以连接到containerd server上则说明检测成功
		if err == nil {
			transientFailureCount = 0
			continue
		}

		select {
		case <-r.shutdownContext.Done():
			r.logger.Info("stopping healthcheck following graceful shutdown")
			monitor.Close()
			return
		default:
		}
		// 否则则说明containerd 没有响应
		r.logger.WithError(err).WithField("binary", binaryName).Debug("daemon is not responding")
		// 没有响应的可能原因: 1.daemon 还没有start 起来
		if r.daemonPid == -1 {
			continue
		}

		transientFailureCount++
		// 小于最大测试次数并且daemon 是active 状态，继续检测
		if transientFailureCount < maxConnectionRetryCount || system.IsProcessAlive(r.daemonPid) {
			continue
		}
		// 充值检测次数
		transientFailureCount = 0
		// 如果containerd daemon 依旧在运行并且达到最大检测次数，则发送信号重启daemon
		if system.IsProcessAlive(r.daemonPid) {
			r.logger.WithField("pid", r.daemonPid).Info("killing and restarting containerd")
			// Try to get a stack trace
			syscall.Kill(r.daemonPid, syscall.SIGUSR1)
			<-time.After(100 * time.Millisecond)
			system.KillProcess(r.daemonPid)
		}
		if r.daemonWaitCh != nil {
			<-r.daemonWaitCh
		}
		// 删除老的address
		os.Remove(r.GRPC.Address)
		if err := r.startContainerd(); err != nil {
			r.logger.WithError(err).Error("failed restarting containerd")
			continue
		}
		// 重新连接到containerd daemon
		if err := monitor.Reconnect(); err != nil {
			r.logger.WithError(err).Error("failed connect to containerd")
			continue
		}

		var wg sync.WaitGroup

		for _, c := range r.clients {
			wg.Add(1)

			go func(c *client) {
				defer wg.Done()
				c.logger.WithField("namespace", c.namespace).Debug("creating new containerd remote client")
				if err := c.reconnect(); err != nil {
					r.logger.WithError(err).Error("failed to connect to containerd")
					// TODO: Better way to handle this?
					// This *shouldn't* happen, but this could wind up where the daemon
					// is not able to communicate with an eventually up containerd
				}
			}(c)

			wg.Wait()
		}
	}
}
