#!/usr/bin/env node

import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import {
  existsSync,
  readFileSync,
  readdirSync,
  statSync,
  writeFileSync,
} from "node:fs";
import { dirname, join, relative, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";

const scriptDirectory = dirname(fileURLToPath(import.meta.url));
const repositoryRoot = resolve(scriptDirectory, "..");
const frontendDirectory = join(repositoryRoot, "frontend");
const outputPath = join(repositoryRoot, "THIRD_PARTY_LICENSES.txt");
const overridesPath = join(scriptDirectory, "licenses", "overrides.json");
const goReleaseTargets = [
  "darwin/amd64",
  "darwin/arm64",
  "freebsd/amd64",
  "freebsd/arm64",
  "linux/386",
  "linux/amd64",
  "linux/arm",
  "linux/arm64",
  "openbsd/amd64",
  "openbsd/arm64",
  "windows/386",
  "windows/amd64",
  "windows/arm64",
];
const goreleaserPath = join(repositoryRoot, ".goreleaser.yml");
const noticeNamePattern =
  /(?:^|[-_.])(authors|copyright|copying|licen[cs]e|notice|patents)(?:[-_.]|$)/i;

function compare(left, right) {
  return left < right ? -1 : left > right ? 1 : 0;
}

function fail(message) {
  throw new Error(message);
}

function sha256(content) {
  return createHash("sha256").update(content).digest("hex");
}

function repositoryRelative(path) {
  return relative(repositoryRoot, path).split(sep).join("/");
}

function run(command, args, cwd, environment = {}) {
  try {
    return execFileSync(command, args, {
      cwd,
      encoding: "utf8",
      maxBuffer: 64 * 1024 * 1024,
      env: { ...process.env, ...environment },
      stdio: ["ignore", "pipe", "pipe"],
    });
  } catch (error) {
    const stderr = error.stderr?.toString().trim();
    fail(
      `${command} ${args.join(" ")} failed${stderr ? `: ${stderr}` : ""}`,
    );
  }
}

function parseJSONStream(source) {
  const values = [];
  let position = 0;

  while (position < source.length) {
    while (/\s/.test(source[position] ?? "")) position += 1;
    if (position >= source.length) break;
    if (source[position] !== "{") {
      fail(`unexpected non-object JSON value at byte ${position}`);
    }

    const start = position;
    let depth = 0;
    let escaped = false;
    let quoted = false;

    for (; position < source.length; position += 1) {
      const character = source[position];
      if (quoted) {
        if (escaped) escaped = false;
        else if (character === "\\") escaped = true;
        else if (character === '"') quoted = false;
        continue;
      }
      if (character === '"') quoted = true;
      else if (character === "{") depth += 1;
      else if (character === "}") {
        depth -= 1;
        if (depth === 0) {
          position += 1;
          break;
        }
      }
    }

    if (depth !== 0 || quoted) fail("truncated JSON stream");
    values.push(JSON.parse(source.slice(start, position)));
  }

  return values;
}

function yamlScalar(value) {
  const trimmed = value.trim();
  if (trimmed.startsWith('"')) return JSON.parse(trimmed);
  if (trimmed.startsWith("'")) {
    if (!trimmed.endsWith("'")) fail(`invalid YAML scalar ${trimmed}`);
    return trimmed.slice(1, -1).replaceAll("''", "'");
  }
  return trimmed;
}

function configuredReleaseTargets() {
  const lines = readFileSync(goreleaserPath, "utf8").split(/\r?\n/);
  const simpleList = (key) => {
    const start = lines.findIndex((line) => line === `    ${key}:`);
    if (start < 0) fail(`missing builds.${key} in .goreleaser.yml`);
    const values = [];
    for (let index = start + 1; index < lines.length; index += 1) {
      const line = lines[index];
      const trimmed = line.trim();
      const indentation = line.length - line.trimStart().length;
      if (trimmed && !trimmed.startsWith("#") && indentation <= 4) break;
      const match = line.match(/^      - ([^:]+)$/);
      if (match) values.push(yamlScalar(match[1]));
    }
    if (values.length === 0) fail(`empty builds.${key} in .goreleaser.yml`);
    return values;
  };

  const operatingSystems = simpleList("goos");
  const architectures = simpleList("goarch");
  const ignoreStart = lines.findIndex((line) => line === "    ignore:");
  if (ignoreStart < 0) fail("missing builds.ignore in .goreleaser.yml");
  const ignored = new Set();
  let ignoredOperatingSystem = "";

  for (let index = ignoreStart + 1; index < lines.length; index += 1) {
    const line = lines[index];
    const trimmed = line.trim();
    const indentation = line.length - line.trimStart().length;
    if (trimmed && !trimmed.startsWith("#") && indentation <= 4) break;
    const goos = line.match(/^      - goos: (.+)$/);
    if (goos) {
      if (ignoredOperatingSystem) {
        fail("ignore entry without goarch in .goreleaser.yml");
      }
      ignoredOperatingSystem = yamlScalar(goos[1]);
      continue;
    }
    const goarch = line.match(/^        goarch: (.+)$/);
    if (goarch) {
      if (!ignoredOperatingSystem) {
        fail("ignore goarch without goos in .goreleaser.yml");
      }
      ignored.add(`${ignoredOperatingSystem}/${yamlScalar(goarch[1])}`);
      ignoredOperatingSystem = "";
    }
  }
  if (ignoredOperatingSystem) {
    fail("ignore entry without goarch in .goreleaser.yml");
  }

  return operatingSystems
    .flatMap((goos) => architectures.map((goarch) => `${goos}/${goarch}`))
    .filter((target) => !ignored.has(target))
    .sort(compare);
}

function validateReleaseTargets() {
  const configured = configuredReleaseTargets();
  const declared = [...goReleaseTargets].sort(compare);
  if (JSON.stringify(configured) !== JSON.stringify(declared)) {
    fail(
      "Go release target list is out of sync with .goreleaser.yml\n" +
        `  generator: ${declared.join(", ")}\n` +
        `  GoReleaser: ${configured.join(", ")}`,
    );
  }
}

function noticeFiles(directory) {
  return readdirSync(directory)
    .filter((name) => noticeNamePattern.test(name))
    .map((name) => ({ name, path: join(directory, name) }))
    .filter(({ path }) => statSync(path).isFile())
    .sort((left, right) => compare(left.name, right.name));
}

const overrideConfiguration = JSON.parse(readFileSync(overridesPath, "utf8"));
if (overrideConfiguration.schemaVersion !== 1) {
  fail(`unsupported override schema in ${repositoryRelative(overridesPath)}`);
}
const configuredOverrides = new Map(
  Object.entries(overrideConfiguration.packages ?? {}),
);
const usedOverrides = new Set();

const packages = new Map();
const texts = new Map();

function getPackage(ecosystem, id, declaredLicense = "") {
  const key = `${ecosystem}:${id}`;
  const existing = packages.get(key);
  if (existing) {
    if (
      declaredLicense &&
      existing.declaredLicense &&
      declaredLicense !== existing.declaredLicense
    ) {
      fail(
        `${id} has conflicting declared licenses: ` +
          `${existing.declaredLicense} and ${declaredLicense}`,
      );
    }
    if (declaredLicense) existing.declaredLicense = declaredLicense;
    return existing;
  }

  const created = {
    declaredLicense,
    ecosystem,
    files: new Map(),
    id,
    notes: [],
  };
  packages.set(key, created);
  return created;
}

function addLicenseText(packageRecord, source, content) {
  const digest = sha256(content);
  const fileKey = `${source}\u0000${digest}`;
  if (packageRecord.files.has(fileKey)) return;

  packageRecord.files.set(fileKey, { digest, source });
  const textRecord = texts.get(digest) ?? {
    content,
    references: new Set(),
  };
  if (!textRecord.content.equals(content)) {
    fail(`SHA-256 collision while processing ${packageRecord.id}`);
  }
  textRecord.references.add(
    `${packageRecord.ecosystem}:${packageRecord.id} (${source})`,
  );
  texts.set(digest, textRecord);
}

function collectGoDependencies() {
  const modules = new Map();

  // The target list is validated against GoReleaser before collection. Its
  // union avoids host-dependent output while retaining only modules linked by
  // at least one h3fuzz release target.
  for (const target of goReleaseTargets) {
    const [goos, goarch] = target.split("/");
    const packageMetadata = parseJSONStream(
      run("go", ["list", "-deps", "-json", "."], repositoryRoot, {
        CGO_ENABLED: "0",
        GOARCH: goarch,
        GOOS: goos,
      }),
    );

    for (const metadata of packageMetadata) {
      const module = metadata.Module;
      if (!module || module.Main) continue;
      if (!module.Path || !module.Version || !module.Dir) {
        fail(`incomplete Go module metadata for ${metadata.ImportPath}`);
      }
      const id = `${module.Path}@${module.Version}`;
      const existing = modules.get(id);
      if (existing && existing.module.Dir !== module.Dir) {
        fail(`${id} resolved to more than one directory`);
      }
      const record = existing ?? { module, targets: new Set() };
      record.targets.add(target);
      modules.set(id, record);
    }
  }

  for (const [id, record] of [...modules.entries()].sort(
    ([left], [right]) => compare(left, right),
  )) {
    const { module, targets } = record;
    const packageRecord = getPackage("go", id);
    const files = noticeFiles(module.Dir);
    if (files.length === 0) {
      fail(`${id} has no root license or notice file`);
    }
    for (const file of files) {
      addLicenseText(packageRecord, file.name, readFileSync(file.path));
    }
    if (module.Replace?.Dir) {
      packageRecord.notes.push(
        `resolved through local replacement ${repositoryRelative(module.Replace.Dir)}`,
      );
    }
    packageRecord.notes.push(`linked targets: ${[...targets].sort(compare).join(", ")}`);
  }
}

function frontendGraph() {
  const report = JSON.parse(
    run(
      "pnpm",
      ["licenses", "list", "--prod", "--json"],
      frontendDirectory,
    ),
  );
  const found = new Map();
  const expected = new Set();

  for (const [licenseGroup, entries] of Object.entries(report)) {
    if (!Array.isArray(entries)) {
      fail(`unexpected pnpm license report group ${licenseGroup}`);
    }
    for (const entry of entries) {
      const declaredLicense = entry.license || licenseGroup;
      for (const version of entry.versions ?? []) {
        expected.add(`${entry.name}@${version}`);
      }
      for (const packagePath of entry.paths ?? []) {
        const metadataPath = join(packagePath, "package.json");
        if (!existsSync(metadataPath)) {
          fail(`pnpm reported a missing package path: ${packagePath}`);
        }
        const metadata = JSON.parse(readFileSync(metadataPath, "utf8"));
        if (metadata.name !== entry.name || !metadata.version) {
          fail(`pnpm metadata mismatch at ${packagePath}`);
        }
        const id = `${metadata.name}@${metadata.version}`;
        if (!expected.has(id)) {
          fail(`${id} was not listed in pnpm's version manifest`);
        }
        const record = found.get(id) ?? {
          declaredLicense,
          directories: new Set(),
        };
        if (record.declaredLicense !== declaredLicense) {
          fail(`${id} has conflicting declared licenses in pnpm output`);
        }
        record.directories.add(packagePath);
        found.set(id, record);
      }
    }
  }

  for (const id of expected) {
    if (!found.has(id)) fail(`pnpm did not provide an installed path for ${id}`);
  }
  return found;
}

function overrideContent(fileConfiguration) {
  const filePath = join(scriptDirectory, "licenses", fileConfiguration.path);
  let content = readFileSync(filePath);
  if (fileConfiguration.stripFinalLF) {
    if (content.at(-1) !== 0x0a) {
      fail(`${repositoryRelative(filePath)} does not end in the expected LF`);
    }
    content = content.subarray(0, content.length - 1);
  }
  if (sha256(content) !== fileConfiguration.sha256) {
    fail(`${repositoryRelative(filePath)} does not match its pinned SHA-256`);
  }
  return content;
}

function collectNestedNotices(
  packageRecord,
  packageDirectory,
  nestedDirectory,
) {
  const root = join(packageDirectory, nestedDirectory);
  if (!existsSync(root) || !statSync(root).isDirectory()) {
    fail(`${packageRecord.id} is missing ${nestedDirectory}`);
  }
  for (const child of readdirSync(root).sort(compare)) {
    const childPath = join(root, child);
    if (!statSync(childPath).isDirectory()) continue;
    const files = noticeFiles(childPath);
    if (files.length === 0) {
      fail(`${packageRecord.id} has no notice in ${nestedDirectory}/${child}`);
    }
    for (const file of files) {
      addLicenseText(
        packageRecord,
        `${nestedDirectory}/${child}/${file.name}`,
        readFileSync(file.path),
      );
    }
  }
}

function collectFrontendDependencies() {
  const graph = frontendGraph();

  for (const [id, dependency] of [...graph.entries()].sort(([left], [right]) =>
    compare(left, right),
  )) {
    const packageRecord = getPackage(
      "frontend",
      id,
      dependency.declaredLicense,
    );
    const rootNotices = new Map();
    for (const directory of [...dependency.directories].sort(compare)) {
      for (const file of noticeFiles(directory)) {
        const content = readFileSync(file.path);
        const existing = rootNotices.get(file.name);
        if (existing && !existing.equals(content)) {
          fail(`${id} has different ${file.name} files across peer contexts`);
        }
        rootNotices.set(file.name, content);
      }
    }

    const override = configuredOverrides.get(id);
    if (rootNotices.size > 0) {
      if (override) fail(`${id} now has a root notice; remove its stale override`);
      for (const [name, content] of [...rootNotices.entries()].sort(
        ([left], [right]) => compare(left, right),
      )) {
        addLicenseText(packageRecord, name, content);
      }
      continue;
    }

    if (!override) {
      fail(`${id} has no root license or notice file and no audited override`);
    }
    if (override.declaredLicense !== dependency.declaredLicense) {
      fail(
        `${id} override expects ${override.declaredLicense}, ` +
          `but pnpm reports ${dependency.declaredLicense}`,
      );
    }
    usedOverrides.add(id);
    packageRecord.notes.push(override.reason);

    for (const file of override.licenseFiles ?? []) {
      addLicenseText(
        packageRecord,
        `audited override: ${file.source}`,
        overrideContent(file),
      );
    }
    for (const directory of [...dependency.directories].sort(compare)) {
      for (const nestedDirectory of override.nestedNoticeDirectories ?? []) {
        collectNestedNotices(packageRecord, directory, nestedDirectory);
      }
    }
  }

  const unused = [...configuredOverrides.keys()]
    .filter((id) => !usedOverrides.has(id))
    .sort(compare);
  if (unused.length > 0) {
    fail(`unused frontend license overrides: ${unused.join(", ")}`);
  }
}

function collectStaticAssets() {
  for (const [id, asset] of Object.entries(
    overrideConfiguration.assets ?? {},
  ).sort(([left], [right]) => compare(left, right))) {
    const assetPath = join(repositoryRoot, asset.path);
    if (!existsSync(assetPath) || !statSync(assetPath).isFile()) {
      fail(`${id} is missing ${asset.path}`);
    }
    const actualDigest = sha256(readFileSync(assetPath));
    if (actualDigest !== asset.sha256) {
      fail(
        `${asset.path} has SHA-256 ${actualDigest}; expected ${asset.sha256}`,
      );
    }

    const packageRecord = getPackage("asset", id, asset.declaredLicense);
    packageRecord.notes.push(
      `source artifact ${asset.sourceArtifact} (SHA-256 ${asset.sourceSha256})`,
    );
    for (const file of asset.licenseFiles ?? []) {
      addLicenseText(
        packageRecord,
        `pinned source: ${file.source}`,
        overrideContent(file),
      );
    }
    if (packageRecord.files.size === 0) {
      fail(`${id} has no configured license text`);
    }
  }
}

function line(value = "") {
  return Buffer.from(`${value}\n`, "utf8");
}

function generateBundle() {
  const chunks = [];
  const append = (value = "") => chunks.push(line(value));
  const sortedPackages = [...packages.values()].sort((left, right) =>
    compare(`${left.ecosystem}:${left.id}`, `${right.ecosystem}:${right.id}`),
  );

  append("H3FUZZ THIRD-PARTY LICENSE BUNDLE");
  append("=================================");
  append();
  append("This file is generated. Do not edit it by hand.");
  append("Regenerate with: node scripts/generate-third-party-licenses.mjs");
  append();
  append("Dependency scopes:");
  append(
    "  Go: union of go list -deps -json . for every GoReleaser target",
  );
  append(`      (${goReleaseTargets.join(", ")}; CGO_ENABLED=0)`);
  append("  Frontend: pnpm licenses list --prod --json");
  append("  Data asset: final.txt, verified against its pinned SHA-256");
  append();
  append(
    "The project license is in LICENSE. This bundle covers linked Go modules and",
  );
  append(
    "the installed frontend production dependency graph and bundled default wordlist.",
  );
  append();
  append("DEPENDENCY MANIFEST");
  append("===================");

  const ecosystemHeadings = {
    asset: ["Bundled data assets", "-------------------"],
    frontend: ["Frontend production packages", "----------------------------"],
    go: ["Go modules", "----------"],
  };
  for (const ecosystem of ["go", "frontend", "asset"]) {
    append();
    append(ecosystemHeadings[ecosystem][0]);
    append(ecosystemHeadings[ecosystem][1]);
    for (const packageRecord of sortedPackages.filter(
      (entry) => entry.ecosystem === ecosystem,
    )) {
      const declared = packageRecord.declaredLicense
        ? ` [declared: ${packageRecord.declaredLicense}]`
        : "";
      append(`  ${packageRecord.id}${declared}`);
    }
  }

  append();
  append("PACKAGE-TO-TEXT MAPPING");
  append("=======================");
  for (const packageRecord of sortedPackages) {
    append();
    append(`${packageRecord.ecosystem}:${packageRecord.id}`);
    for (const file of [...packageRecord.files.values()].sort((left, right) =>
      compare(`${left.digest}:${left.source}`, `${right.digest}:${right.source}`),
    )) {
      append(`  T-${file.digest}  ${file.source}`);
    }
    for (const note of packageRecord.notes.sort(compare)) {
      append(`  NOTE: ${note}`);
    }
  }

  append();
  append("LICENSE AND NOTICE TEXTS");
  append("========================");
  append();
  append(
    "Identical byte sequences are emitted once. Every package remains mapped to",
  );
  append("the complete SHA-256 identifier above and in each text section below.");

  for (const [digest, textRecord] of [...texts.entries()].sort(
    ([left], [right]) => compare(left, right),
  )) {
    append();
    append("-------------------------------------------------------------------------------");
    append(`T-${digest}`);
    append(`SHA-256: ${digest}`);
    append("Used by:");
    for (const reference of [...textRecord.references].sort(compare)) {
      append(`  ${reference}`);
    }
    append("BEGIN VERBATIM TEXT");
    chunks.push(textRecord.content);
    if (textRecord.content.at(-1) !== 0x0a) chunks.push(line());
    append("END VERBATIM TEXT");
  }

  return Buffer.concat(chunks);
}

try {
  validateReleaseTargets();
  collectGoDependencies();
  collectFrontendDependencies();
  collectStaticAssets();
  writeFileSync(outputPath, generateBundle());
  process.stdout.write(
    `wrote ${repositoryRelative(outputPath)} ` +
      `(${packages.size} dependencies, ${texts.size} unique texts)\n`,
  );
} catch (error) {
  process.stderr.write(`license generation failed: ${error.message}\n`);
  process.exitCode = 1;
}
