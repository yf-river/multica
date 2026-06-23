import { existsSync, readFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";

const repoRoot = path.resolve(import.meta.dirname, "..");
const opikRoot = existsSync("/data/ida/opik") ? "/data/ida/opik" : "/data/ida/opik-local-demo";
const docsPath = path.join(repoRoot, "apps/docs/content/docs/production-observability.zh.mdx");
const pagePath = path.join(repoRoot, "packages/views/prompt-library/components/prompt-library-page.tsx");

const mapping = [
  ["提示词库", "prompt-library", "PromptLibraryPage"],
  ["提示词调试场", "prompt-playground", "运行并记录"],
  ["智能体调试场", "agent-playground", "创建真实智能体 任务"],
  ["数据集", "prompt_evaluation_asset", "dataset_row_count"],
  ["测试套件", "prompt_evaluation_asset", "test_suite_case_count"],
  ["实验", "prompt_evaluation_asset", "experiment_dimension_count"],
  ["优化运行", "asset_type: \"优化运行\"", "优化运行"],
  ["运行历史", "prompt_evaluation_run", "RunEvidencePanel"],
];

const opikExists = existsSync(opikRoot);
const docs = existsSync(docsPath) ? readFileSync(docsPath, "utf8") : "";
const page = existsSync(pagePath) ? readFileSync(pagePath, "utf8") : "";
const missingPageTerms = mapping
  .map(([feature, , marker]) => ({ feature, marker }))
  .filter(({ marker }) => !page.includes(marker));

const requiredDocTerms = ["Opik", "Multica 对应", "验收证据", "训练与评估"];
const missingDocTerms = requiredDocTerms.filter((term) => !docs.includes(term));
const evidence = {
  schema: "multica.opik_mapping_evidence.v1",
  opik_root: opikRoot,
  opik_source_exists: opikExists,
  docs_path: docsPath,
  docs_has_mapping: missingDocTerms.length === 0,
  missing_doc_terms: missingDocTerms,
  page_path: pagePath,
  missing_page_markers: missingPageTerms,
  mapping: mapping.map(([opikFeature, multicaConcept, evidenceAnchor]) => ({
    "Opik功能": opikFeature,
    "Multica对应": multicaConcept,
    "验收证据锚点": evidenceAnchor,
  })),
};

console.log(JSON.stringify(evidence, null, 2));
if (!opikExists || missingDocTerms.length > 0 || missingPageTerms.length > 0) {
  process.exit(1);
}
