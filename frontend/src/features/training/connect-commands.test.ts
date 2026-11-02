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
