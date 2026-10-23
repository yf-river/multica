// 验收种子的清理规则：导入后按清单清空执行态、凭据和租约，并检查新增执行型表是否被遗漏。
import fs from 'node:fs';

export function loadRules(file) {
  const rules = JSON.parse(fs.readFileSync(file, 'utf8'));
  if (!Array.isArray(rules.rules) || !rules.rules.length) throw Error('清理清单缺少规则');
  for (const rule of rules.rules) {
    if (!rule.table || !/^[a-z_][a-z0-9_]*$/.test(rule.table) || typeof rule.sql !== 'string' || !rule.sql.trim()) {
      throw Error('清理规则格式错误: '+JSON.stringify(rule));
    }
  }
  if (!Array.isArray(rules.coverage?.patterns) || !rules.coverage.patterns.length) throw Error('清理清单缺少覆盖范围');
  return rules;
}

// 覆盖范围用 SQL LIKE 写法（% 任意长度、_ 单字符），其余字符按字面匹配。
export function likeMatch(value, pattern) {
  const source = pattern
    .split(/([%_])/)
    .map((part) => part === '%' ? '.*' : part === '_' ? '.' : part.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'))
    .join('');
  return new RegExp('^'+source+'$').test(value);
}

// 返回命中覆盖范围却没有规则、也没有显式豁免的表；结果非空时恢复必须停止。
export function uncoveredTables(tables, rules) {
  const covered = new Set(rules.rules.map((rule) => rule.table));
  const exempt = new Set(Object.keys(rules.coverage.exempt || {}));
  return [...new Set(tables)].sort().filter((name) =>
    rules.coverage.patterns.some((pattern) => likeMatch(name, pattern)) && !covered.has(name) && !exempt.has(name));
}

// 豁免表名不再存在时提示清理，避免改名后旧豁免掩盖新表。
export function staleExemptions(tables, rules) {
  const present = new Set(tables);
  return Object.keys(rules.coverage.exempt || {}).filter((name) => !present.has(name)).sort();
}
