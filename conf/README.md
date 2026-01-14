# Configuration Directory

This directory is intended for your local configuration files. 

## Usage

1. Copy a sample configuration from the `../examples/` directory.
   ```bash
   cp ../examples/replicate.sample.yaml ./my_config.yaml
   ```
2. Modify the file with your specific settings (Redis addresses, passwords, etc.).
3. Run the application pointing to this configuration file.

## Note

Files in this directory (extensions .yaml, .toml, .json) are ignored by Git to prevent accidental commitment of sensitive information (passwords, tokens).
