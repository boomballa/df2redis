# JDK 21 Setup Guide

[中文文档](../jdk-setup.md)

df2redis builds depend on Camellia and redis-rdb-cli, which in turn require **JDK 21**. This English companion distills the installation steps.

## macOS

- **Homebrew** – `brew install temurin` (or `brew install --cask temurin@21` depending on the tap). Update `JAVA_HOME` to the installed path.
- **PKG installer** – download the Temurin PKG, run it, and export `JAVA_HOME=/Library/Java/JavaVirtualMachines/temurin-21.jdk/Contents/Home`.

## Linux

- Debian/Ubuntu: use the Adoptium apt repo and install `temurin-21-jdk`.
- CentOS/RHEL: download the tarball, extract to `/usr/lib/jvm/temurin-21`, and update the `alternatives` entries.
- Other distros: fetch Temurin/Zulu/Liberica tarballs and set `JAVA_HOME` + `PATH` manually.

## Windows

Download the Temurin MSI installer, enable "Set JAVA_HOME" during setup, and verify via `java -version`.

Troubleshooting notes (missing compiler, PKIX errors, shells not sourcing profiles) remain in the Chinese document.
