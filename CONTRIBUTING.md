# Contributing to df2redis

Thank you for your interest in contributing to **df2redis**! We welcome contributions from everyone. This document will guide you through the process of setting up your development environment, running tests, and submitting your changes.

## 🛠️ Development Setup

### Prerequisites

- **Go**: Version 1.21 or higher.
- **Docker**: For running integration tests (Dragonfly & Redis instances).
- **Make** (Optional): For running convenience scripts (if added).

### Installation

1.  **Fork** the repository on GitHub.
2.  **Clone** your fork locally:
    ```bash
    git clone https://github.com/your-username/df2redis.git
    cd df2redis
    ```
3.  **Install dependencies**:
    ```bash
    go mod download
    ```

## 🧪 Running Tests

We prioritize engineering quality. All contributions should pass existing tests and include new tests where appropriate.

### Unit Tests
Run the standard unit test suite:
```bash
go test -v ./internal/...
```

### Integration Tests
We have a full replication integration test suite in `tests/integration`.

1.  **Start Dependencies** (Dragonfly & Redis):
    ```bash
    # Example using Docker
    docker run -d -p 6379:6379 --name dragonfly docker.dragonflydb.io/dragonflydb/dragonfly
    docker run -d -p 6380:6379 --name redis redis
    ```

2.  **Configure Test**:
    Copy the sample configuration:
    ```bash
    cp tests/integration.sample.yaml tests/integration.yaml
    # Edit tests/integration.yaml if your ports differ from defaults
    ```

3.  **Run Integration Test**:
    ```bash
    go test -v ./tests/integration
    ```

## 📐 Coding Guidelines

- **Style**: We follow standard Go conventions (gofmt).
- **Logging**: Use strict structured logging.
- **Commits**: Please follow [Conventional Commits](https://www.conventionalcommits.org/).
  - `feat: add new replication mode`
  - `fix: resolve handshake timeout`
  - `docs: update architecture diagram`

## 🚀 Submitting a Pull Request

1.  Create a new branch: `git checkout -b feat/my-feature`.
2.  Make your changes and write tests.
3.  Run `go test ./...` to ensure everything is green.
4.  Push to your fork and submit a Pull Request.
5.  Wait for review! We aim to review PRs within 48 hours.

## 🐛 Reporting Bugs

If you find a bug, please open an issue with:
- `df2redis` version
- Dragonfly & Redis versions
- Reproduction steps or config (sanitized)
- Logs (if applicable)

---
Happy Coding! 🚀
