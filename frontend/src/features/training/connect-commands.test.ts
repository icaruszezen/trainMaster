import { describe, expect, it } from "vitest"

import { connectCommands } from "@/features/training/connect-commands"

describe("connectCommands", () => {
  const serverUrl = "http://192.168.1.10:8080"

  it("includes the token placeholder when the platform requires one", () => {
    expect(connectCommands({ serverUrl, tokenRequired: true })).toEqual({
      local: "make setup && make up",
      remoteEnv: "TM_SERVER_URL=http://192.168.1.10:8080\nTM_NODE_TOKEN=<令牌>",
      remoteStart: "make setup\ndocker compose up -d trainer",
      bareMetal:
        "trainer/scripts/install.sh --server-url http://192.168.1.10:8080 --token <令牌> --systemd",
    })
  })

  it("leaves the token out when the platform does not require one", () => {
    const commands = connectCommands({ serverUrl, tokenRequired: false })
    expect(commands.remoteEnv).toBe("TM_SERVER_URL=http://192.168.1.10:8080")
    expect(commands.bareMetal).toBe(
      "trainer/scripts/install.sh --server-url http://192.168.1.10:8080 --systemd"
    )
  })
})
