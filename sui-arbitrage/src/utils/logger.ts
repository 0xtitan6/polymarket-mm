import pino from "pino";

let _logger: pino.Logger | undefined;

export function initLogger(level: string): pino.Logger {
  _logger = pino({
    level,
    transport:
      process.env["NODE_ENV"] !== "production"
        ? { target: "pino-pretty", options: { colorize: true } }
        : undefined,
  });
  return _logger;
}

export function getLogger(): pino.Logger {
  if (!_logger) {
    _logger = initLogger("info");
  }
  return _logger;
}
