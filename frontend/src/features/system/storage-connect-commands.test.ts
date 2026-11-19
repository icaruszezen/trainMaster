import { describe, expect, it } from "vitest"

import {
  DEFAULT_STORAGE_PORT,
  generateStorageToken,
  parseStoragePort,
  SAVED_STORAGE_TOKEN_PLACEHOLDER,
  STORAGE_TOKEN_PLACEHOLDER,
  storageConnectCommands,
} from "@/features/system/storage-connect-commands"

describe("storageConnectCommands", () => {
  it("parses explicit and default storage container ports safely", () => {
    expect(parseStoragePort("")).toBe(DEFAULT_STORAGE_PORT)
    expect(parseStoragePort("192.168.1.50")).toBe(DEFAULT_STORAGE_PORT)
    expect(parseStoragePort("http://192.168.1.50")).toBe(80)
    expect(parseStoragePort("http://192.168.1.50/")).toBe(80)
    expect(parseStoragePort("192.168.1.50:80")).toBe(80)
    expect(parseStoragePort("http://192.168.1.50:8090")).toBe(8090)
    expect(parseStoragePort("http://10.0.0.12:9190/")).toBe(9190)
    expect(parseStoragePort("192.168.1.50:18090")).toBe(18090)
    expect(parseStoragePort("https://storage.internal.local")).toBe(443)
    expect(parseStoragePort(":::invalid-url")).toBe(DEFAULT_STORAGE_PORT)
  })

  it("builds docker compose and docker run commands with token and port", () => {
    const withToken = storageConnectCommands({
      endpoint: "http://192.168.1.50:9095",
      token: "tok_abc123",
    })
    expect(withToken.port).toBe(9095)
    expect(withToken.dockerCompose).toContain("TM_STORAGE_PORT=9095")
    expect(withToken.dockerCompose).toContain("TM_STORAGE_TOKEN=tok_abc123")
    expect(withToken.dockerCompose).toContain(
      "docker compose -f docker-compose.storage.yml up -d"
    )
    expect(withToken.dockerRun).toContain("-p 9095:8090")
    expect(withToken.dockerRun).toContain("-e TM_STORAGE_TOKEN=tok_abc123")

    const blankNew = storageConnectCommands({
      endpoint: "http://192.168.1.50:8090",
      token: "",
      hasSavedSecret: false,
    })
    expect(blankNew.dockerCompose).toContain(
      `TM_STORAGE_TOKEN='${STORAGE_TOKEN_PLACEHOLDER}'`
    )

    const blankSaved = storageConnectCommands({
      endpoint: "http://192.168.1.50:8090",
      token: "",
      hasSavedSecret: true,
    })
    expect(blankSaved.dockerRun).toContain(
      `-e TM_STORAGE_TOKEN='${SAVED_STORAGE_TOKEN_PLACEHOLDER}'`
    )
  })

  it("generates hex storage tokens of expected length", () => {
    const tok1 = generateStorageToken(24)
    const tok2 = generateStorageToken(24)
    expect(tok1).toMatch(/^[0-9a-f]{48}$/)
    expect(tok2).toMatch(/^[0-9a-f]{48}$/)
    expect(tok1).not.toBe(tok2)
  })
})
