import packageJson from "../../package.json";

const currentYear = new Date().getFullYear();

export const APP_CONFIG = {
  name: "jcyber",
  version: packageJson.version,
  copyright: `© ${currentYear}, jcyber.`,
  meta: {
    title: "jcyber — Autonomous Pentest Console",
    description: "LLM-driven autonomous penetration testing console",
  },
};
