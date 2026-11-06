import { describe, expect, it } from "vitest"

import { connectCommands } from "@/features/training/connect-commands"

describe("connectCommands", () => {
  const serverUrl = "http://192.168.1.10:8080"

  it("includes the token placeholder when the platform requires one", () => {
    expect(connectCommands({ serverUrl, tokenRequired: true })).toEqual({
      local: "make setup && make up",
      remoteSetup:
        'make setup SETUP_ARGS="--server-url http://192.168.1.10:8080 --token <令牌>"',
      remoteStart: "docker compose up -d --build trainer",
      bareMetal:
        "sudo trainer/scripts/install.sh --server-url http://192.168.1.10:8080 --token <令牌> --systemd",
      windows:
        "& ([scriptblock]::Create((irm 'http://192.168.1.10:8080/api/v1/compute/windows/install.ps1'))) -ServerUrl 'http://192.168.1.10:8080' -Token '<令牌>' -InstallPython -Service",
      windowsFromRepo:
        "powershell -ExecutionPolicy Bypass -File trainer\\scripts\\install.ps1 -ServerUrl 'http://192.168.1.10:8080' -Token '<令牌>' -InstallPython -Service",
    })
  })

  it("leaves the token out when the platform does not require one", () => {
    const commands = connectCommands({ serverUrl, tokenRequired: false })
    expect(commands.remoteSetup).toBe(
      'make setup SETUP_ARGS="--server-url http://192.168.1.10:8080"'
    )
    expect(commands.bareMetal).toBe(
      "sudo trainer/scripts/install.sh --server-url http://192.168.1.10:8080 --systemd"
    )
    expect(commands.windows).toBe(
      "& ([scriptblock]::Create((irm 'http://192.168.1.10:8080/api/v1/compute/windows/install.ps1'))) -ServerUrl 'http://192.168.1.10:8080' -InstallPython -Service"
    )
    expect(commands.windowsFromRepo).toBe(
      "powershell -ExecutionPolicy Bypass -File trainer\\scripts\\install.ps1 -ServerUrl 'http://192.168.1.10:8080' -InstallPython -Service"
    )
  })

  it("enables TLS 1.2 before downloading the Windows installer over https", () => {
    // Windows PowerShell 5.1 does not enable TLS 1.2 by default.
    const { windows, windowsFromRepo } = connectCommands({
      serverUrl: "https://train.example.com",
      tokenRequired: true,
    })
    expect(windows).toBe(
      "[Net.ServicePointManager]::SecurityProtocol = 'Tls12'; & ([scriptblock]::Create((irm 'https://train.example.com/api/v1/compute/windows/install.ps1'))) -ServerUrl 'https://train.example.com' -Token '<令牌>' -InstallPython -Service"
    )
    // The script from the repository downloads nothing before it starts.
    expect(windowsFromRepo).toBe(
      "powershell -ExecutionPolicy Bypass -File trainer\\scripts\\install.ps1 -ServerUrl 'https://train.example.com' -Token '<令牌>' -InstallPython -Service"
    )
    // Plain http needs no TLS.
    expect(connectCommands({ serverUrl, tokenRequired: true }).windows).toMatch(
      /^& /
    )
  })

  it("doubles single quotes in the Windows commands' PowerShell strings", () => {
    const { windows, windowsFromRepo } = connectCommands({
      serverUrl: "http://it's.example:8080",
      tokenRequired: false,
    })
    expect(windows).toBe(
      "& ([scriptblock]::Create((irm 'http://it''s.example:8080/api/v1/compute/windows/install.ps1'))) -ServerUrl 'http://it''s.example:8080' -InstallPython -Service"
    )
    expect(windowsFromRepo).toBe(
      "powershell -ExecutionPolicy Bypass -File trainer\\scripts\\install.ps1 -ServerUrl 'http://it''s.example:8080' -InstallPython -Service"
    )
  })

  it("keeps the server URL placeholder inside the PowerShell quotes", () => {
    // Pages opened through localhost show a placeholder host to replace.
    const { windows } = connectCommands({
      serverUrl: "http://<平台服务器 IP>:8080",
      tokenRequired: true,
    })
    expect(windows).toContain(
      "(irm 'http://<平台服务器 IP>:8080/api/v1/compute/windows/install.ps1')"
    )
    expect(windows).toContain("-ServerUrl 'http://<平台服务器 IP>:8080'")
  })

  it("re-enables this machine's trainer after --mode off", () => {
    // make setup keeps TM_TRAINER_MODE=off from .env, so `make setup && make up`
    // would start no training node.
    const off = connectCommands({
      serverUrl,
      tokenRequired: true,
      localTrainerMode: "off",
    })
    expect(off.local).toBe('make up SETUP_ARGS="--mode auto"')
    // Other machines have their own .env.
    expect(off.remoteSetup).toBe(
      'make setup SETUP_ARGS="--server-url http://192.168.1.10:8080 --token <令牌>"'
    )
    for (const localTrainerMode of ["auto", "gpu", "cpu", null] as const) {
      expect(
        connectCommands({ serverUrl, tokenRequired: true, localTrainerMode })
          .local
      ).toBe("make setup && make up")
    }
  })

  it("starts only the trainer on other machines, rebuilding its image", () => {
    const { remoteSetup, remoteStart } = connectCommands({
      serverUrl,
      tokenRequired: true,
    })
    // make setup chooses the CUDA version (and creates .env) before the build.
    expect(remoteSetup).toMatch(/^make setup /)
    // A service name keeps compose from starting postgres and app there too;
    // --build picks up a new variant or a git pull.
    expect(remoteStart).toBe("docker compose up -d --build trainer")
    expect(remoteStart).not.toMatch(/make up/)
  })
})
