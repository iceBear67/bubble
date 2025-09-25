# bubble
An SSH daemon that forwards terminal to a docker container, or create a new one on-demand.  
Work in progress. More tests needed.

# Build
Ensure you have Go 1.24.1 (tested) and Git installed.

```bash
$ git clone https://github.com/iceBear67/bubble
$ bash ./bubble/build.sh
building client.go
building daemon.go
$ ls target
client daemon
```

By default, artifacts are built with CGO disabled, making them runnable without glibc.

# Usage

Access to the Docker socket and a pre-generated SSH private key is required.

```aiignore
$ ./target/daemon -h
Usage of ./target/daemon:
  -config string
        Path to config file (default "config.yml")
  -help
        Show help
# ./target/daemon
(Logs are omitted)
# ssh test@localhost -p xxxx
Creating a new container, please wait...
Redirecting to the new container..
[root@workspace-test] #
```
Building your own [workspace image](https://github.com/iceBear67/workspace-docker) is recommended.

Example configuration:
```yaml
# Address for the daemon to listen on. Required.
address: ":2333"

# Newly created workspaces will join this Docker network. Optional.
# If empty, this feature is disabled.
network-group: "workspace"

# Server private key file. Required.
# Generate an SSH key pair via: `sshd-keygen -t rsa -b 4096 -f ssh_host_key -N ""`
server-key-file: "ssh_host_key"

# Newly created workspaces will mount %workspace-parent%/%workspace-name% to /mnt/workspace. Optional.
# If empty, this feature is disabled.
workspace-parent: "workspace"

global-share-dir: "global"

# List of allowed SSH keys (~/.sshd/authorized_keys).
# If empty, anyone can connect.
# Since 0.2, keys should be named.
keys: 
  icybear: 
    - "...."

# Manager server helps you managing container itself from the container inside.
# It starts a HTTP server on that port and listens signal from containers who enabled manager.
# The server has a IP whitelist which is maintained by bubble. 
# Since 0.3, manager server has deprecated unix-socket and turned to TCP. 
manager:
  # Changing this to 127.0.0.1 will break everything.
  address: "0.0.0.0:7684"

# Since 0.2, accesses to containers should be explicitly declared to named keys
access-control:
  icybear: 
    patterns:
      - "^icybear$"

# Container configurations based on SSH username.
templates:
  ".*":  # Regex matching the username.
    # Pro tip: Build your own workspace image.
    image: "debian:11"

    # The program that runs on every new connection.
    # Pro tip: Use tmux.
    exec: ["/bin/bash"]
    
    cmd: ['/bin/bash']

    # Environment variables.
    env:
      - "UID=114514"

    # Remove the container when it stops.
    rm: true
    
    # Enable the manager feature. workspace-data must be present.
    enable-manager: true

    # Enable privileged mode. Required for Docker-in-Docker.
    # Warning: This introduces security risks.
    privilege: true

    # Enable port forwarding. Containers may send a PORT request to manager server to open ports.
    port-forwarding:
      min-port: 0
      max-port: 65535
```

# Client

Once `enable-manager` is enabled，You can use `client` executable to destroy or stop the current workspace.
```bash
$ client
Usage: client <destroy|stop> 
```

## SFTP

Due to the isolation nature of containers, bubble cannot interact files within your containers directly. However, SFTP communicates over stdin/stdout, which yields some workaround:
1. Link your sftp-server implementation to `/usr/sbin/bubble-sftp`. Bubble will execute this executable when sftp is requested.
2. For containers that aren't specialized for using in bubble, add option `-s /path/to/sftp-server` to sftp cli.

## Port mapping
This feature is very experimental, check the usage from bubble client script.

# Roadmap
 - ~~Support SFTP.~~ Implemented.
   - You can install `openssh-sftp-server` (debian) on your container then use `-s /usr/lib/openssh/sftp-server` on your sftp commandline.
 - ~~Port mapping~~ Implemented.