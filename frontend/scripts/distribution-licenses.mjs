import { createHash } from 'node:crypto'
import { existsSync, readFileSync, readdirSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const frontend = fileURLToPath(new URL('../', import.meta.url))
const projectNoticePath = 'assets/weknora-licenses.txt'
export const npmNoticePath = 'assets/npm-licenses.md'
const spritePath = 'tdesign-icons/0.4.1/fonts/index.js'
const widgetPath = 'weknora-widget.js'
const novncName = '@novnc/novnc'
const novncSource = 'https://github.com/novnc/noVNC/archive/refs/tags/v1.7.0.tar.gz'
const novncPackage = 'https://registry.npmjs.org/@novnc/novnc/-/novnc-1.7.0.tgz'

function readText(file) {
  const text = readFileSync(file, 'utf8').replaceAll('\r\n', '\n')
  if (!text.trim()) throw new Error(`Empty license input or build output: ${file}`)
  return text
}

function readProjectLicense() {
  const license = readText(join(frontend, 'licenses/WeKnora-MIT.txt')).trim()
  const rootLicensePath = join(frontend, '../LICENSE')
  // Docker's build context starts at frontend/. In full checkouts, verify
  // its checked-in copy of the MIT block against the repository license.
  if (existsSync(rootLicensePath)) {
    const rootLicense = readText(rootLicensePath)
    const notice = rootLicense.match(/^Copyright \(C\) .*Tencent.*$/m)?.[0].trim()
    const grant = rootLicense.match(/Terms of the MIT:\s*-+\s*([\s\S]+?)\n\s*Dependencies and Licenses:/)?.[1].trim()
    if (license !== `${notice}\n\n${grant}`) {
      throw new Error('frontend/licenses/WeKnora-MIT.txt differs from the MIT block in ../LICENSE')
    }
  }
  return license
}

function legalComment(text) {
  return `/*!\n${text}\n*/\n`
}

export function projectLicenseBanner() {
  return legalComment(
    `WeKnora (MIT)\n\n${readProjectLicense()}\n\n` +
    `Third-party licenses and corresponding source: /${projectNoticePath}, /${npmNoticePath}`,
  )
}

function readNovncNotices() {
  const packageDir = join(frontend, 'node_modules', novncName)
  const { version } = JSON.parse(readText(join(packageDir, 'package.json')))
  if (version !== '1.7.0') {
    throw new Error(`Review the noVNC license and matching source link before bundling ${novncName}@${version}`)
  }
  const files = ['LICENSE.txt', 'AUTHORS', 'vendor/pako/LICENSE',
    ...readdirSync(join(packageDir, 'docs'))
      .filter((name) => name.startsWith('LICENSE.')).sort().map((name) => `docs/${name}`),
  ]
  return [
    `${novncName} ${version} (MPL-2.0 and accompanying licenses)`,
    `Corresponding source for the unmodified noVNC code: ${novncSource}`,
    `Exact npm package used for this build: ${novncPackage}`,
    ...files.map((file) => `${file}\n${readText(join(packageDir, file)).trim()}`),
  ].join('\n\n')
}

export function distributionLicenses() {
  return {
    name: 'weknora-distribution-licenses',
    apply: 'build',
    generateBundle(_options, bundle) {
      const sprite = readText(join(frontend, 'public', spritePath))
      if (createHash('sha256').update(sprite).digest('hex') !==
          '99a963d7a1f8bb7d8253bc77cc6fa1ab51bfd3f403029b150fdbb778f5ca22bb') {
        throw new Error('The vendored TDesign icon sprite changed. Review its source and license before bundling it.')
      }
      const spriteLicense = readText(join(frontend, 'licenses/TDesign-Icons-MIT.txt')).trim()
      const browserSkill = readText(join(frontend, 'src/assets/browserskill/LICENSE')).trim()
      const projectNotices = [
        `WeKnora (MIT)\n\n${readProjectLicense()}`,
        `BrowserSkill logo\n\n${browserSkill}`,
        `TDesign Icons 0.4.1 static sprite\n` +
        `Source: https://tdesign.gtimg.com/icon/0.4.1/fonts/index.js\n\n${spriteLicense}`,
        readNovncNotices(),
        ...readPackageNotices(bundle),
        `Licenses of bundled npm packages: /${npmNoticePath}`,
      ].join('\n\n')
      this.emitFile({ type: 'asset', fileName: projectNoticePath, source: `${projectNotices}\n` })
      this.emitFile({
        type: 'asset', fileName: widgetPath,
        source: projectLicenseBanner() + readText(join(frontend, 'public', widgetPath)),
      })
      this.emitFile({ type: 'asset', fileName: spritePath, source: legalComment(spriteLicense) + sprite })
    },
    writeBundle(options) {
      verifyDistributionLicenses(options.dir)
    },
  }
}

// Vite records the first license file in each bundled package. Also preserve
// any separately supplied package-root notices.
function readPackageNotices(bundle) {
  const roots = new Set()
  for (const output of Object.values(bundle)) {
    if (output.type !== 'chunk') continue
    for (const id of output.moduleIds) {
      const path = id.replaceAll('\\', '/')
      const marker = '/node_modules/'
      const position = path.lastIndexOf(marker)
      if (position === -1 || id.startsWith('\0')) continue
      const relative = path.slice(position + marker.length)
      const [scopeOrName, name] = relative.split('/')
      const packageName = scopeOrName.startsWith('@') ? `${scopeOrName}/${name}` : scopeOrName
      if (packageName !== novncName) {
        roots.add(path.slice(0, position + marker.length) + packageName)
      }
    }
  }
  return [...roots].sort().flatMap((root) => {
    const files = readdirSync(root, { withFileTypes: true })
      .filter((file) => file.isFile() && /^(notice|copyright|authors)(?:[._-]|$)/i.test(file.name))
      .map((file) => file.name).sort()
    if (!files.length) return []
    const { name, version } = JSON.parse(readText(join(root, 'package.json')))
    return files.map((file) => `${name} ${version} ${file}\n${readText(join(root, file)).trim()}`)
  })
}

export function verifyDistributionLicenses(directory = join(frontend, 'dist')) {
  const report = readText(join(directory, projectNoticePath))
  const npmReport = readText(join(directory, npmNoticePath))
  if (!report.includes(readProjectLicense()) || !report.includes('BrowserSkill logo') ||
      !report.includes('TDesign Icons 0.4.1') || !report.includes(novncSource) ||
      !report.includes('Mozilla Public License Version 2.0') ||
      !npmReport.includes('## @novnc/novnc - 1.7.0 (MPL-2.0)')) {
    throw new Error(`Incomplete browser license bundle in ${directory}`)
  }
  const banner = projectLicenseBanner().trimEnd()
  for (const page of ['index.html', 'embed.html']) {
    const html = readText(join(directory, page))
    const entry = html.match(/<script\b[^>]*type="module"[^>]*src="([^"]+)"/)?.[1]
    if (!entry || !readText(resolve(directory, entry.replace(/^\//, ''))).includes(banner)) {
      throw new Error(`Missing project license in the bundled entry for ${page}`)
    }
  }
  if (!readText(join(directory, widgetPath)).startsWith(banner) ||
      !readText(join(directory, spritePath)).includes('Copyright (c) 2021-present TDesign')) {
    throw new Error(`Missing project or TDesign license in standalone browser assets in ${directory}`)
  }
}
