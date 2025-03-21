package daemon

import (
	"context"
	"fmt"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"log"
	"strings"
)

const ContainerStatusRunning = "running"
const ContainerStatusCreated = "created"
const ContainerStatusPaused = "paused"
const ContainerStatusExited = "exited"
const ContainerStatusUp = "up"
const InContainerWorkspaceDir = "/workspace"

func SetupDockerClient() (*client.Client, error) {
	var err error
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return dockerClient, nil
}

func setupNetworkGroup(client *client.Client, networkName string) {
	ctx := context.Background()
	list, err := client.NetworkList(ctx, network.ListOptions{})
	if err != nil {
		log.Fatal(err)
	}
	for _, summary := range list {
		if summary.Name == networkName {
			return
		}
	}
	_, err = client.NetworkCreate(ctx, networkName, network.CreateOptions{})
	if err != nil {
		log.Fatalf("Cannot create network %v", err)
	}
}

func cleanStatusCode(status string) string {
	array := strings.Split(strings.ToLower(status), " ")
	if len(array) == 0 {
		return status
	}
	return array[0]
}

func ContainerExists(dockerClient *client.Client, name string) (exists bool, status string, id string) {
	containers, err := dockerClient.ContainerList(context.Background(), container.ListOptions{All: true})
	if err != nil {
		log.Println("Failed to list containers:", err)
		return false, "", ""
	}
	for _, cont := range containers {
		for _, n := range cont.Names {
			if strings.TrimPrefix(n, "/") == name {

				return true, cleanStatusCode(cont.Status), cont.ID
			}
		}
	}
	return false, "", ""
}

func CreateContainer(
	dockerClient *client.Client,
	user string,
	workspaceDir string,
	networkGroup string,
	containerTemplate *ContainerConfig,
) (string, error) {
	ctx := context.Background()
	containerName := "workspace-" + user
	containerConfig := &container.Config{
		Image:    containerTemplate.Image,
		Tty:      true,
		Cmd:      []string{containerTemplate.Exec},
		Hostname: containerName,
		Env:      containerTemplate.Env,
	}
	var volumes []string
	copy(containerTemplate.Volumes, volumes)
	// todo create management socket
	if workspaceDir != "" {
		volumes = append(volumes, workspaceDir+":/workspace")
	}
	hostConfig := &container.HostConfig{
		Binds:      volumes,
		AutoRemove: containerTemplate.Rm,
		Privileged: containerTemplate.Privilege,
	}
	var networkConfig *network.NetworkingConfig = nil
	if networkGroup != "" {
		nwg := networkGroup
		inspect, err := dockerClient.ContainerInspect(ctx, containerName)
		shouldSet := true
		if err == nil && inspect.NetworkSettings != nil {
			for _, settings := range inspect.NetworkSettings.Networks {
				if settings.NetworkID == nwg {
					shouldSet = false
				}
			}
		}
		if shouldSet {
			networkConfig = &network.NetworkingConfig{
				EndpointsConfig: map[string]*network.EndpointSettings{
					"network": {
						NetworkID: nwg,
					},
				},
			}
		}
	}

	resp, err := dockerClient.ContainerCreate(ctx, containerConfig, hostConfig, networkConfig, nil, containerName)
	if err != nil {
		return "", fmt.Errorf("failed to create container: %v", err)
	}
	if err := dockerClient.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return "", fmt.Errorf("failed to start container: %v", err)
	}
	return resp.ID, nil
}
