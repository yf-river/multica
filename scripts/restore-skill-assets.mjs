// 先校验备份并恢复不可变对象；数据库恢复由现有运维流程单独执行。
import fs from 'node:fs';import path from 'node:path';import crypto from 'node:crypto';
const [backup,uploads,action='verify']=process.argv.slice(2);
if(!backup||!uploads||!['verify','restore'].includes(action))throw Error('用法: node restore-skill-assets.mjs <backup> <upload-dir> [verify|restore]');
const manifest=JSON.parse(fs.readFileSync(path.join(backup,'manifest.json')));
for(const object of manifest.objects){
 if(!object.key.startsWith('private/skills/')||object.key.includes('..'))throw Error('非法存储键');
 const from=path.join(backup,'objects',object.key);const data=fs.readFileSync(from);
 if(data.length!==object.size||crypto.createHash('sha256').update(data).digest('hex')!==object.sha256)throw Error('备份损坏: '+object.key);
}
if(action==='restore')for(const object of manifest.objects){
 const from=path.join(backup,'objects',object.key),to=path.join(uploads,object.key);
 fs.mkdirSync(path.dirname(to),{recursive:true});const temporary=to+'.restore';fs.copyFileSync(from,temporary);fs.renameSync(temporary,to);
}
console.log(JSON.stringify({action,objects:manifest.objects.length,verified:true}));
