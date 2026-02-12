# 部署指南 (Deployment Guide)

本文提供了在生产环境中部署 **df2redis** 的最佳实践，包括 Docker 容器化部署和 Systemd 服务管理。

## 1. Docker 容器化部署 🐳

### 方式 A: Docker Compose (推荐)

这是最快捷的体验方式，可以一键拉起 Dragonfly (Source)、Redis (Target) 和 df2redis (Replicator)。

1.  **创建目录与文件**

    ```bash
    mkdir df2redis-deploy && cd df2redis-deploy
    touch docker-compose.yml config.yaml
    ```

2.  **编写 `docker-compose.yml`**

    ```yaml
    version: '3.8'

    services:
      # 源端：Dragonfly
      dragonfly:
        image: docker.dragonflydb.io/dragonflydb/dragonfly
        ports:
          - "6379:6379"
        ulimits:
          memlock: -1

      # 目标端：Redis
      redis:
        image: redis:7.0
        ports:
          - "6380:6379"

      # 复制工具：df2redis
      replicator:
        image: ghcr.io/your-username/df2redis:latest
        # 如果使用本地构建的二进制，可以使用 volumes 挂载
        # volumes:
        #   - ./bin/df2redis:/app/df2redis
        volumes:
          - ./config.yaml:/app/config.yaml
          - ./data:/app/data
        command: ["/app/df2redis", "replicate", "--config", "/app/config.yaml"]
        depends_on:
          - dragonfly
          - redis
        restart: always
    ```

3.  **编写 `config.yaml`**

    > 注意：在 Docker 网络中，请使用服务名（如 `dragonfly`, `redis`）作为地址。

    ```yaml
    source:
      addr: "dragonfly:6379"  # 使用 docker-compose service name
      password: ""

    target:
      type: "redis-standalone"
      addr: "redis:6379"
      password: ""

    checkpoint:
      dir: "/app/data"        # 映射到宿主机，确保持久化
    ```

4.  **启动服务**

    ```bash
    docker-compose up -d
    docker-compose logs -f replicator
    ```

---

## 2. Linux Systemd 服务管理 🐧

对于生产环境的物理机/虚拟机部署，建议使用 Systemd 进行进程管理。

1.  **准备二进制与配置**

    ```bash
    # 1. 下载或编译二进制
    sudo cp bin/df2redis /usr/local/bin/
    sudo chmod +x /usr/local/bin/df2redis

    # 2. 创建配置目录
    sudo mkdir -p /etc/df2redis
    sudo cp config.yaml /etc/df2redis/config.yaml

    # 3. 创建数据目录
    sudo mkdir -p /var/lib/df2redis
    sudo chown nobody:nobody /var/lib/df2redis
    ```

2.  **创建 Systemd Unit 文件**

    编辑 `/etc/systemd/system/df2redis.service`：

    ```ini
    [Unit]
    Description=df2redis Replication Service
    Documentation=https://github.com/your-username/df2redis
    After=network.target

    [Service]
    Type=simple
    User=nobody
    Group=nobody
    
    # 运行命令
    ExecStart=/usr/local/bin/df2redis replicate --config /etc/df2redis/config.yaml
    
    # 工作目录（用于存放 checkpoint）
    WorkingDirectory=/var/lib/df2redis
    
    # 自动重启策略
    Restart=on-failure
    RestartSec=5s
    
    # 日志输出到 journald
    StandardOutput=journal
    StandardError=journal
    
    # 文件描述符限制
    LimitNOFILE=65536

    [Install]
    WantedBy=multi-user.target
    ```

3.  **启动服务**

    ```bash
    # 重新加载配置
    sudo systemctl daemon-reload

    # 启动服务
    sudo systemctl start df2redis

    # 设置开机自启
    sudo systemctl enable df2redis

    # 查看状态
    sudo systemctl status df2redis

    # 查看日志
    journalctl -u df2redis -f
    ```

---

## 3. Kubernetes 部署 (Helm Chart 简述) ☸️

*(待补充)* 目前建议使用 `Deployment` + `ConfigMap` 的方式部署，参考 `docker-compose.yml` 的配置逻辑。
