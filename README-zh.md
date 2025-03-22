# bubble
一个根据来访用户名自动连接 Docker 容器的 SSH 服务端。  
开发中

# 构建
确保你已经安装了 Go 1.24.1 (测试过) 和 Git

```bash
$ git clone https://github.com/iceBear67/bubble
$ bash ./build.sh
building client.go
building daemon.go
$ ls target
client daemon
```

默认情况下构建时不使用 CGO，因此你应该可以在不依赖 glibc 的条件下使用它们（对于容器比较有用）

# 用法

你需要先安装 Docker 并且确保你可以访问到 Docker socket，以及需要生成一个 SSH 私钥以供程序使用。

```aiignore
$ ./target/daemon
Usage of ./target/daemon:
  -config string
        Path to config file (default "config.yml")
  -help
        Show help
```

实例配置:
```yaml
# 守护进程监听的地址。必填。
address: ":2333"

# 新创建的工作区将加入此 Docker 网络。可选。
# 如果为空，则该功能不会启用。
network-group: "workspace"

# 服务器私钥文件。必填。
# 生成 SSH 密钥对命令：`sshd-keygen -t rsa -b 4096 -f ssh_host_key -N ""`
server-key-file: "ssh_host_key"

# 新创建的工作区会将 %workspace-data%/%workspace-name% 挂载到 /workspace。可选。
# 如果为空，则该功能不会启用。
workspace-data: "workspace"

# 允许的 SSH 公钥列表（~/.sshd/authorized_keys）。
# 如果为空，任何人都可以连接。
keys: []

# 根据 SSH 用户名选择容器配置。
templates:
  ".*":  # 匹配用户名的正则表达式。
    # 提示：你可以参考 example/Dockerfile 构建自己的工作区镜像。
    image: "debian:11"

    # 每次新连接时运行的程序。
    # 提示：建议使用 tmux。
    exec: ["/bin/bash"]
    
    cmd: ['/bin/bash']

    # 环境变量。
    env:
      - "UID=114514"

    # 容器停止时自动删除。
    rm: true
    
    # 启用客户端功能。该功能必须和 workspace-data 搭配使用
    enable-manager: true

    # 启用特权模式。Docker-in-Docker 需要开启此选项。
    # 警告：启用此选项可能带来安全风险。
    privilege: true
```

## 客户端

`enable-manager` 启用后，守护进程会在容器的 `/workspace` 下挂载一个 unix socket 用于容器内进程和守护进程通信。  
可以通过在容器内使用上一步构建出的 `client` 可执行文件来销毁/停止当前容器。
```bash
$ client
Usage: client <destroy|stop> 
```