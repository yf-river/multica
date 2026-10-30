import {build} from 'esbuild'
import {createRequire} from 'node:module'
import {dirname,join} from 'node:path'
import {readFile,writeFile,mkdir,copyFile} from 'node:fs/promises'
await build({entryPoints:['../context-insights/src/index.ts'],outfile:'dist/insights-core.js',bundle:true,platform:'node',format:'esm',target:'node22',external:['@deepseek-ai/dsh-session']})
const path='dist/index.js'
await writeFile(path,(await readFile(path,'utf8')).replaceAll("'@multica/context-insights'","'./insights-core.js'"))

await mkdir('dist/third-party',{recursive:true})
await copyFile('../context-insights/LICENSE','dist/third-party/dsh-context-LICENSE')
await copyFile('../context-insights/src/official/LICENSE','dist/third-party/dsh-session-stats-LICENSE')
const require=createRequire(new URL('../../context-insights/package.json',import.meta.url))
for(const name of ['zod','@deepseek-ai/schemastery'])await copyFile(join(dirname(require.resolve(name+'/package.json')),'LICENSE'),'dist/third-party/'+name.replaceAll('/','-')+'-LICENSE')
await writeFile('dist/third-party/SOURCE.json',JSON.stringify({dsh_context:{version:'0.78.2',commit:'402742a885a7aaf030e17b280627871cd5238de6'},dsh_session_stats:{version:'0.2.1-rc.3'}},null,2)+'\n')
