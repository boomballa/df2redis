### TODO

- investigate why `rmt -m redis://10.46.128.24:6397` hits `java.net.SocketException: Connection reset` during import (check auth, firewall, or use nodes.conf).
- once target connectivity is resolved, rerun `./bin/df2redis migrate --config examples/migrate.sample.yaml --show 8080` to validate full pipeline.
