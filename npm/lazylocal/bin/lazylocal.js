#!/usr/bin/env node

const fs = require('fs');
const path = require('path');
const os = require('os');
const { spawn, execSync } = require('child_process');

const VERSION = '0.1.1';

function getBinaryName() {
  const platform = process.platform;
  const arch = process.arch;

  if (platform !== 'darwin' && platform !== 'linux' && platform !== 'win32') {
    console.error(`[lazylocal] Unsupported platform: ${platform}. Only macOS, Linux, and Windows are supported.`);
    process.exit(1);
  }

  const osName = platform === 'win32' ? 'windows' : platform;
  const goarch = arch === 'arm64' ? 'arm64' : (arch === 'x64' ? 'amd64' : null);
  if (!goarch) {
    console.error(`[lazylocal] Unsupported architecture: ${arch}. Only x64 and arm64 are supported.`);
    process.exit(1);
  }

  const ext = platform === 'win32' ? '.exe' : '';
  return `lazylocal_${VERSION}_${osName}_${goarch}${ext}`;
}

function findBinary() {
  const isWin = process.platform === 'win32';
  const binName = isWin ? 'lazylocal.exe' : 'lazylocal';

  // 1. Check local repository development build: ../../../dist/lazylocal[.exe]
  const localRepoBin = path.resolve(__dirname, '../../../dist', binName);
  if (fs.existsSync(localRepoBin)) {
    return localRepoBin;
  }

  // Also check ../../../dist/release/lazylocal-windows-amd64.exe etc.
  const releaseDir = path.resolve(__dirname, '../../../dist/release');
  if (fs.existsSync(releaseDir)) {
    const arch = process.arch === 'arm64' ? 'arm64' : 'amd64';
    const osPrefix = isWin ? 'windows' : (process.platform === 'darwin' ? 'darwin' : 'linux');
    const cand = path.join(releaseDir, `lazylocal-${osPrefix}-${arch}${isWin ? '.exe' : ''}`);
    if (fs.existsSync(cand)) {
      return cand;
    }
  }

  // 2. Check current working directory ./dist/lazylocal[.exe]
  const cwdBin = path.resolve(process.cwd(), 'dist', binName);
  if (fs.existsSync(cwdBin)) {
    return cwdBin;
  }

  // 3. Check ~/.local/bin and ~/.lazylocal/bin
  const candidateDirs = [
    path.join(os.homedir(), '.local', 'bin'),
    path.join(os.homedir(), '.lazylocal', 'bin')
  ];
  for (const dir of candidateDirs) {
    const candidate = path.join(dir, binName);
    if (fs.existsSync(candidate)) {
      return candidate;
    }
  }

  // 4. Check if available on system PATH
  try {
    const whichCmd = isWin ? 'where' : 'which';
    const nullDev = isWin ? '2>nul' : '2>/dev/null';
    const rawOut = execSync(`${whichCmd} ${binName} ${nullDev}`, { encoding: 'utf8' }).trim();
    if (rawOut) {
      const firstLine = rawOut.split(/\r?\n/)[0].trim();
      if (firstLine && fs.existsSync(firstLine) && firstLine !== __filename) {
        return firstLine;
      }
    }
  } catch (e) {
    // Not in PATH
  }

  // 5. If Go compiler is installed locally, build on the fly into ~/.local/bin
  try {
    const nullDev = isWin ? '2>nul' : '2>/dev/null';
    const hasGo = execSync(`go version ${nullDev}`, { encoding: 'utf8' });
    if (hasGo) {
      const targetDir = path.join(os.homedir(), '.local', 'bin');
      fs.mkdirSync(targetDir, { recursive: true });
      console.log(`[lazylocal] Building native binary from source...`);
      execSync(`go install github.com/kuarezma/lazylocal/cmd/lazylocal@latest`, { stdio: 'inherit' });
      const gopath = execSync('go env GOPATH', { encoding: 'utf8' }).trim() || path.join(os.homedir(), 'go');
      const builtPath = path.join(gopath, 'bin', binName);
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
    const isWin = process.platform === 'win32';
    console.error(`[lazylocal] Native binary not found.
Please build or install lazylocal using one of the following methods:
  1) Local build:  go build -o dist/lazylocal${isWin ? '.exe' : ''} ./cmd/lazylocal
  2) Go install:   go install github.com/kuarezma/lazylocal/cmd/lazylocal@latest
  3) Installer:    ${isWin ? 'irm https://raw.githubusercontent.com/kuarezma/lazylocal/main/install.ps1 | iex' : 'curl -fsSL https://raw.githubusercontent.com/kuarezma/lazylocal/main/install.sh | sh'}
  4) Homebrew:     brew install kuarezma/tap/lazylocal (macOS/Linux)
`);
    process.exit(1);
  }

  if (process.platform !== 'win32') {
    try {
      fs.chmodSync(binaryPath, 0o755);
    } catch (e) {
      // Ignore chmod error if not owner
    }
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
  if (process.platform !== 'win32') {
    process.on('SIGTERM', () => child.kill('SIGTERM'));
  }
}

main();
