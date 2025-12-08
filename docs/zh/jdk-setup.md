# JDK 21 安装指南

[English Version](en/jdk-setup.md) | [中文版](jdk-setup.md)

df2redis 依赖 Camellia 与 redis-rdb-cli 源码，在构建这些模块时要求 **JDK 21**。本指南整理常见平台的安装方式，帮助你快速准备好满足要求的 Java 环境。

> 提示：JDK 18 及以下版本无法满足 `--release 21` 的编译配置，构建时会报 “无效的目标发行版: 21”。

## 通用校验步骤

无论采用哪种方式安装，完成后请执行：

```bash
java -version
javac -version
```

若输出类似 `openjdk version "21.0.x"`、`javac 21.0.x`，说明环境已经就绪。

## macOS

### 使用 Homebrew（推荐）

```bash
brew update
brew install --cask temurin@21

export JAVA_HOME=$(/usr/libexec/java_home -v 21)
export PATH="$JAVA_HOME/bin:$PATH"
```

可将 `JAVA_HOME`/`PATH` 写入 `~/.zprofile` 或 `~/.bash_profile` 以长期生效。

### 使用官方 PKG 安装包

1. 访问 [Temurin Releases](https://adoptium.net/zh-CN/temurin/releases/?version=21) 下载 macOS 安装包（`.pkg`）。
2. 双击安装后，执行：
   ```bash
   export JAVA_HOME=$(/usr/libexec/java_home -v 21)
   export PATH="$JAVA_HOME/bin:$PATH"
   ```

## Linux

### Debian / Ubuntu

```bash
sudo apt-get update
sudo apt-get install -y temurin-21-jdk   # 若仓库无此包，可使用 adoptium 官方脚本或 tar 包

export JAVA_HOME=/usr/lib/jvm/temurin-21-jdk-amd64
export PATH=$JAVA_HOME/bin:$PATH
```

### CentOS / RHEL / Rocky / AlmaLinux

官方仓库通常仅提供 OpenJDK 8/11。可以使用 Temurin 官方 tar 包：

```bash
cd /usr/local
sudo tar -xzf OpenJDK21U-jdk_x64_linux_hotspot_21.0.4_7.tar.gz   # 先下载好 tar 包
sudo mv jdk-21.0.4+7 temurin-21                                  # 可自定义目录名

export JAVA_HOME=/usr/local/temurin-21
export PATH=$JAVA_HOME/bin:$PATH

java -version
javac -version
```

如需与系统 `alternatives` 集成：

```bash
sudo alternatives --install /usr/bin/java java /usr/local/temurin-21/bin/java 211
sudo alternatives --install /usr/bin/javac javac /usr/local/temurin-21/bin/javac 211
sudo alternatives --config java
sudo alternatives --config javac
```

### 其它发行版

Temurin、Zulu、Liberica 等发行版均提供 JDK 21，下载对应平台的 tar 包或包管理器脚本即可。务必确认 `JAVA_HOME` 指向解压后的 JDK 根目录，并将 `bin` 加入 `PATH`。

## Windows（如需）

1. 访问 [Temurin Releases](https://adoptium.net/zh-CN/temurin/releases/?version=21&os=windows) 下载 `.msi` 安装包。
2. 安装过程中勾选 “Set JAVA_HOME variable”。
3. 重新打开终端，执行 `java -version` 验证。

## 常见问题

- **`No compiler is provided in this environment`**：说明 Maven 找不到 `javac`，通常是 `JAVA_HOME` 指向 JRE 而非 JDK。请切换到上述 JDK 21 目录。
- **`PKIX path building failed`**：出现在旧版本 JDK 的 HTTPS 证书校验上。升级至 JDK 21 后即可修复。
- **环境变量在非交互 shell 不生效**：确保将 `JAVA_HOME`、`PATH` 等配置写入 `~/.bash_profile`、`~/.profile` 或系统级文件，并重新登录。

准备好 JDK 21 后，再执行项目构建命令：

```bash
cd df2redis/camellia
./mvnw -pl camellia-redis-proxy/camellia-redis-proxy-bootstrap -am package
```

若需要跳过测试，可加 `-DskipTests`。
