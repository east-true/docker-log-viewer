const dockerTimestampPattern = /^(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?Z)\s/;

export function parseDockerLogLine(message, fallback) {
  const match = message.match(dockerTimestampPattern);
  return {
    sourceTimestamp: match?.[1] || "",
    observedAt: fallback,
    message: match ? message.slice(match[0].length) : message,
  };
}

export class LogAssembler {
  constructor(maxPendingLength = 256 * 1024) {
    this.maxPendingLength = maxPendingLength;
    this.pending = new Map();
  }

  push(event) {
    const key = `${event.container_id}:${event.stream}`;
    const previous = this.pending.get(key);
    let text = `${previous?.text || ""}${event.message}`;
    const metadata = {
      containerId: event.container_id,
      containerName: event.container_name,
      stream: event.stream,
      observedAt: event.observed_at,
    };
    const records = [];

    while (true) {
      const newline = text.indexOf("\n");
      if (newline >= 0) {
        records.push(this.#record(text.slice(0, newline), metadata));
        text = text.slice(newline + 1);
        continue;
      }
      if (text.length > this.maxPendingLength) {
        records.push(this.#record(`${text.slice(0, this.maxPendingLength)} … [긴 로그 계속]`, metadata));
        text = text.slice(this.maxPendingLength);
        continue;
      }
      break;
    }

    if (text) this.pending.set(key, { text, ...metadata });
    else this.pending.delete(key);
    return records;
  }

  flush() {
    const records = [...this.pending.values()].map(({ text, ...metadata }) => this.#record(text, metadata));
    this.pending.clear();
    return records;
  }

  reset() {
    this.pending.clear();
  }

  #record(line, metadata) {
    return { ...metadata, ...parseDockerLogLine(line, metadata.observedAt) };
  }
}
