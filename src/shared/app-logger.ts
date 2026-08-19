import { config } from "./config.js";
import { createLogger } from "./utils/logger.js";

export const logger = createLogger(
  config.log.level,
  config.log.mode,
  config.service.name,
);
