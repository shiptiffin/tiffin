import fs from "node:fs";
export default { project: fs.readFileSync("/etc/passwd", "utf8") };
