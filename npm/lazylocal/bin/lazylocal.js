#!/usr/bin/env node

const fs = require('fs');
const path = require('path');
const os = require('os');
const { spawn, execSync } = require('child_process');

const VERSION = '0.1.0';

function getBinaryName() {
  const platform = process.platform;
  const arch = process.arch;

  if (platform !== 'darwin' && platform !== 'linux') {
    console.error(`[lazylocal] Unsupported platform: ${platform}. Only macOS (darwin) and Linux are supported.`);
    process.exit(1);
  }

  const goarch = arch === 'arm64' ? 'arm64' : (arch === 'x64' ? 'amd64' : null);
  if (!goarch) {
    console.error(`[lazylocal] Unsupported architecture: ${arch}. Only x64 and arm64 are supported.`);
    process.exit(1);
  }

  return `lazylocal_${VERSION}_${platform}_${goarch}`;
}

function findBinary() {
  // 1. Check local repository development build: ../../../dist/lazylocal
  const localRepoBin = path.resolve(__dirname, '../../../dist/lazylocal');
  if (fs.existsSync(localRepoBin)) {
    return localRepoBin;
  }

  // 2. Check current working directory ./dist/lazylocal
  const cwdBin = path.resolve(process.cwd(), 'dist/lazylocal');
  if (fs.existsSync(cwdBin)) {
    return cwdBin;
  }

  // 3. Check ~/.lazylocal/bin/lazylocal
  const userBin = path.join(os.homedir(), '.lazylocal', 'bin', 'lazylocal');
  if (fs.existsSync(userBin)) {
    return userBin;
  }

  // 4. Check if available on system PATH
  try {
    const whichCmd = process.platform === 'win32' ? 'where' : 'which';
    const sysPath = execSync(`${whichCmd} lazylocal 2>/dev/null`, { encoding: 'utf8' }).trim();
    if (sysPath && fs.existsSync(sysPath) && sysPath !== __filename) {
      return sysPath;
    }
  } catch (e) {
    // Not in PATH
  }

  // 5. If Go compiler is installed locally, build on the fly into ~/.lazylocal/bin
  try {
    const hasGo = execSync('go version 2>/dev/null', { encoding: 'utf8' });
    if (hasGo) {
      const targetDir = path.join(os.homedir(), '.lazylocal', 'bin');
      fs.mkdirSync(targetDir, { recursive: true });
      const targetPath = path.join(targetDir, 'lazylocal');
      console.log(`[lazylocal] Building native binary from source...`);
      execSync(`go install github.com/kuarezma/lazylocal/cmd/lazylocal@latest`, { stdio: 'inherit' });
      const gopath = execSync('go env GOPATH', { encoding: 'utf8' }).trim() || path.join(os.homedir(), 'go');
      const builtPath = path.join(gopath, 'bin', 'lazylocal');
      if (fs.existsSync(builtPath)) {
        return builtPath;
      }
    }
  } catch (e) {
    // go build fallback failed
  }

  return null;
}

function main() {
  const binaryPath = findBinary();

  if (!binaryPath) {
    console.error(`[lazylocal] Native binary not found.
Please build or install lazylocal using one of the following methods:
  1) Local build:  make build  (or: go build -o dist/lazylocal ./cmd/lazylocal)
  2) Go install:   go install github.com/kuarezma/lazylocal/cmd/lazylocal@latest
  3) Homebrew:     brew install ugur/tap/lazylocal
`);
    process.exit(1);
  }

  try {
    fs.chmodSync(binaryPath, 0o755);
  } catch (e) {
    // Ignore chmod error if not owner
  }

  const child = spawn(binaryPath, process.argv.slice(2), {
    stdio: 'inherit',
    env: process.env,
  });

  child.on('error', (err) => {
    console.error(`[lazylocal] Failed to launch binary: ${err.message}`);
    process.exit(1);
  });

  child.on('close', (code, signal) => {
    if (signal) {
      process.kill(process.pid, signal);
    } else {
      process.exit(code || 0);
    }
  });

  process.on('SIGINT', () => child.kill('SIGINT'));
  process.on('SIGTERM', () => child.kill('SIGTERM'));
}

main();
