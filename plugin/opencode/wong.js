// The wong plugin for OpenCode 1 and 2: it runs `wong hook` and hands on what wong says. wong setup installs it.
import { spawn } from "node:child_process"
import { homedir } from "node:os"
import { join } from "node:path"

const tools = new Set(["read", "write", "edit", "bash", "shell", "apply_patch"])
const writes = new Set(["write", "edit", "apply_patch"])
const slashes = (path) => path.replaceAll("\\", "/")
const brain = slashes(join(homedir(), ".wong"))
const inBrain = (paths) => paths.length > 0 && paths.every((p) => typeof p === "string" && slashes(p).startsWith(`${brain}/`))

function wong(event, input) {
  return new Promise((resolve) => {
    let out = ""
    let child
    try {
      child = spawn("wong", ["hook", event, "--agent", "opencode"], { stdio: ["pipe", "pipe", "ignore"] })
    } catch {
      return resolve("")
    }
    const timer = setTimeout(() => child.kill(), 30_000)
    child.stdout.on("data", (data) => (out += data))
    child.on("error", () => {
      clearTimeout(timer)
      resolve("")
    })
    child.on("close", () => {
      clearTimeout(timer)
      resolve(out.trim())
    })
    child.stdin.on("error", () => {})
    child.stdin.end(JSON.stringify(input))
  })
}

function sessions(directory) {
  const started = new Map()
  return (sessionID) => {
    if (!started.has(sessionID)) started.set(sessionID, wong("session-start", { session_id: sessionID, cwd: directory }))
    return started.get(sessionID)
  }
}

async function notes(directory, sessionID, tool, args) {
  if (!tools.has(tool)) return ""
  const input = { session_id: sessionID, cwd: directory, tool_name: tool, tool_input: args ?? {} }
  const said = [await wong("pre-tool-use", input)]
  if (writes.has(tool)) said.push(await wong("post-tool-use", input))
  return said.filter(Boolean).join("\n\n")
}

export default {
  id: "wong",

  // OpenCode 2
  async setup(ctx) {
    const directory = ctx.location.directory
    const start = sessions(directory)
    await ctx.session.hook("context", async (event) => {
      const text = await start(event.sessionID)
      if (text) event.system.push({ type: "text", text })
    })
    await ctx.tool.hook("execute.after", async (event) => {
      if (event.status !== "completed") return
      const text = await notes(directory, event.sessionID, event.tool, event.input)
      if (!text) return
      const content = event.result.content ?? []
      event.result = {
        ...event.result,
        content: typeof content === "string" ? `${content}\n\n${text}` : [...content, { type: "text", text }],
      }
    })
    await ctx.permission.hook("evaluate", (event) => {
      if (event.action === "external_directory" && inBrain(event.resources)) event.effect = "allow"
    })
    await ctx.session.hook("experimental.idle", async (event) => {
      await wong("session-end", { session_id: event.sessionID, cwd: directory })
    })
  },

  // OpenCode 1
  async server({ directory }) {
    const start = sessions(directory)
    return {
      "experimental.chat.system.transform": async (input, output) => {
        if (!input.sessionID) return
        const text = await start(input.sessionID)
        if (text) output.system.push(text)
      },
      "tool.execute.after": async (input, output) => {
        const text = await notes(directory, input.sessionID, input.tool, input.args)
        if (text) output.output = `${output.output}\n\n${text}`
      },
      config: async (config) => {
        const rules = config.permission?.external_directory
        config.permission = {
          ...config.permission,
          external_directory: { ...(typeof rules === "string" ? { "*": rules } : rules), [`${brain}/**`]: "allow" },
        }
      },
      event: async ({ event }) => {
        if (event.type === "session.idle") await wong("session-end", { session_id: event.properties.sessionID, cwd: directory })
      },
    }
  },
}
