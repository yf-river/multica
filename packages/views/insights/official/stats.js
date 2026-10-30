// 固定官方源码片段，来源及适配边界见 SOURCE.json。
import * as react from "react";
import * as react_dom from "react-dom";
import * as react_jsx_runtime from "react/jsx-runtime";
import * as _deepseek_ai_dsh_client_ui_primitives from "@deepseek-ai/dsh-client-ui-primitives";

var stat_dialog_module_css_default = {
			"details": "bRhRbq_details",
			"panel": "bRhRbq_panel",
			"reasoning": "bRhRbq_reasoning",
			"route": "bRhRbq_route",
			"title": "bRhRbq_title",
			"titleLabel": "bRhRbq_titleLabel",
			"titleRule": "bRhRbq_titleRule",
			"titleValue": "bRhRbq_titleValue"
		};
var StatsPills_module_css_default = {
			"anchor": "bOPqQW_anchor",
			"label": "bOPqQW_label",
			"pill": "bOPqQW_pill",
			"sep": "bOPqQW_sep"
		};
		/**
		* Compact token count: 517 / 12.2K / 517K / 1.2M.
		* @param value - non-negative token count.
		* @param t - Chat locale seat.
		* @returns locale-owned compact display string.
		*/
		function formatTokens(value, t) {
			const scaled = (candidate) => candidate >= 100 ? String(Math.round(candidate)) : String(Math.round(candidate * 10) / 10);
			if (value < 1e3) return String(value);
			if (value < 1e6) return t("number.thousand", { value: scaled(value / 1e3) });
			return t("number.million", { value: scaled(value / 1e6) });
		}
		/**
		* Exact integer token count with locale-owned digit grouping.
		* @param value - non-negative safe integer token count.
		* @param t - Chat locale seat.
		* @returns an unrounded display string.
		*/
		function formatExactTokens(value, t) {
			const digits = String(value);
			const groups = [];
			for (let end = digits.length; end > 0; end -= 3) groups.unshift(digits.slice(Math.max(0, end - 3), end));
			return groups.join(t("number.groupSeparator"));
		}
		/** Round a cache-read ratio to exact percentage units, with positive ties rounded up. */
		function roundedPercentUnits(cacheReadTokens, denominator, decimalPlaces) {
			const scale = (decimalPlaces === 0 ? 1 : 10) * 100;
			const doubledScale = scale * 2;
			const denominatorQuotient = Math.floor(denominator / doubledScale);
			const denominatorRemainder = denominator % doubledScale;
			let lower = 0;
			let upper = scale;
			while (lower < upper) {
				const candidate = Math.floor((lower + upper + 1) / 2);
				const factor = candidate * 2 - 1;
				if (cacheReadTokens >= factor * denominatorQuotient + Math.ceil(factor * denominatorRemainder / doubledScale)) lower = candidate;
				else upper = candidate - 1;
			}
			return lower;
		}
		function displayPercentUnits(units, decimalPlaces) {
			if (decimalPlaces === 0) return String(units);
			const whole = Math.floor(units / 10);
			const tenths = units % 10;
			return tenths === 0 ? String(whole) : `${whole}.${tenths}`;
		}
		/**
		* Display-ready cache-hit share without rounding a partial hit to 100%.
		* @param cacheReadTokens - exact prompt tokens served from cache.
		* @param promptTokens - exact aggregate prompt tokens.
		* @param decimalPlaces - ordinary-ratio precision; partial hits that would
		* round to 100 automatically use enough additional precision to stay honest.
		* @returns percentage text, or null when there was no prompt input.
		*/
		function formatCacheHitPercent(cacheReadTokens, promptTokens, decimalPlaces = 0) {
			if (promptTokens === 0) return null;
			const missedInputTokens = promptTokens - cacheReadTokens;
			if (missedInputTokens === 0) return "100";
			const roundedUnits = roundedPercentUnits(cacheReadTokens, promptTokens, decimalPlaces);
			if (roundedUnits < (decimalPlaces === 0 ? 100 : 1e3)) return displayPercentUnits(roundedUnits, decimalPlaces);
			let distinguishingPlaces = 1;
			let scaledDoubleGap = missedInputTokens * 200;
			const denominatorTens = Math.floor(promptTokens / 10);
			while (scaledDoubleGap <= denominatorTens) {
				scaledDoubleGap *= 10;
				distinguishingPlaces += 1;
			}
			const denominatorOnes = promptTokens % 10;
			let roundedLoss = 5;
			for (let loss = 1; loss < 5; loss += 1) {
				const factor = loss * 2 + 1;
				const threshold = factor * denominatorTens + Math.floor(factor * denominatorOnes / 10);
				if (scaledDoubleGap <= threshold) {
					roundedLoss = loss;
					break;
				}
			}
			return `99.${"9".repeat(distinguishingPlaces - 1)}${10 - roundedLoss}`;
		}
		/** Viewport margin the placement clamp keeps (the Menu portal margin). */
		const PANEL_MARGIN = 12;
		/** Distance between the trigger's top edge and the panel's bottom. */
		const PANEL_GAP = 8;
		/**
		* Unplaced portal panel: hidden but laid out so the clamp measures real
		* dimensions (the `useAnchoredPosition` measure pass).
		*/
		const MEASURE_STYLE = {
			visibility: "hidden",
			left: 0,
			top: 0
		};
		/**
		* One trigger-anchored dialog seat: open state, viewport-clamped placement, outside-close.
		* @returns the seat; spread `pos ?? MEASURE_STYLE` onto the portaled panel.
		*/
		function useStatDialog() {
			const [open, setOpen] = (0, react.useState)(false);
			const rootRef = (0, react.useRef)(null);
			const panelRef = (0, react.useRef)(null);
			const pos = (0, _deepseek_ai_dsh_client_ui_primitives.useAnchoredPosition)({
				open,
				anchorRef: rootRef,
				panelRef,
				side: "top",
				gap: PANEL_GAP,
				margin: PANEL_MARGIN
			});
			(0, _deepseek_ai_dsh_client_ui_primitives.useDismissOnOutsidePointer)(rootRef, open, setOpen, panelRef);
			(0, react.useEffect)(() => {
				if (!open) return;
				const onKeyDown = (e) => {
					if (e.key === "Escape") setOpen(false);
				};
				const onClick = (e) => {
					if (e.target instanceof Node && rootRef.current?.contains(e.target) !== true && panelRef.current?.contains(e.target) !== true) setOpen(false);
				};
				document.addEventListener("keydown", onKeyDown);
				document.addEventListener("click", onClick, true);
				return () => {
					document.removeEventListener("keydown", onKeyDown);
					document.removeEventListener("click", onClick, true);
				};
			}, [open, setOpen]);
			return {
				open,
				setOpen,
				rootRef,
				panelRef,
				pos
			};
		}
function formatTokensPerSecond(tps) {
			const clamped = Math.max(0, tps);
			return clamped >= 10 ? String(Math.round(clamped)) : String(Math.round(clamped * 10) / 10);
		}
		/**
		* Fold assistant and tool-result nodes into window-scoped display totals —
		* the FALLBACK for assemblies without the `sessionStats` projection.
		*
		* Every displayed figure rides that durable whole-log projection (and token
		* accounting rides `tokenUsage`) because the window is paged and compaction
		* rewrites it; this fold answers "what is on screen" only when no projection
		* value is served. Its field names deliberately mirror the projection's so
		* the two swap wholesale.
		* @param nodes - snapshot nodes.
		* @returns fallback counts and summed wall times.
		*/

		/**
		* Compact duration: 45.2s under a minute, 2m42s from there on.
		* @param ms - duration in milliseconds.
		* @returns display string.
		*/
		function formatDuration(ms, t) {
			const s = ms / 1e3;
			if (s < 60) return t("duration.compactSeconds", { seconds: Math.round(s * 10) / 10 });
			const whole = Math.round(s);
			return t("duration.compactMinutes", {
				minutes: Math.floor(whole / 60),
				seconds: whole % 60
			});
		}
		/**
		* Display-ready cache-hit share of prompt-side input over the whole durable log.
		* @param usage - the session's token-usage projection value.
		* @returns integer text when integer rounding stays below 100, otherwise the
		* minimum decimal precision that still rounds below 100; a full hit returns
		* 100, and no billed input returns null.
		*/
		function cacheHitPercent(usage) {
			const denominator = billedInputTokens(usage);
			return formatCacheHitPercent(usage.cacheReadTokens, denominator);
		}
		/**
		* Sum the three disjoint prompt-side billing buckets.
		* @param usage - the session's token-usage projection value.
		* @returns billed input tokens.
		*/
		function billedInputTokens(usage) {
			return usage.uncachedInputTokens + usage.cacheReadTokens + usage.cacheWriteTokens;
		}
		function exactCount(value, t) {
			return t("message.turnUsage.count", { count: formatExactTokens(value, t) });
		}
		function joined$1(first, second) {
			if (second === null) return first;
			return (0, react_jsx_runtime.jsxs)(react_jsx_runtime.Fragment, { children: [
				first,
				(0, react_jsx_runtime.jsx)("span", {
					className: StatsPills_module_css_default.sep,
					"aria-hidden": true,
					children: "·"
				}),
				second
			] });
		}
		function useSessionStats(useChat, useProjection) {
			
			const projected = useProjection("sessionStats");
			return projected;
		}
		function decodeSpeed(stats, t) {
			return t("message.tokensPerSecond", { tps: formatTokensPerSecond(stats.decodeTokens / (stats.decodeMs / 1e3)) });
		}
		/** A static reading: used when the pill has no dialog rows or the mode is Compact. */
		function PlainPill({ stat, icon, label }) {
			return (0, react_jsx_runtime.jsx)("span", {
				className: StatsPills_module_css_default.anchor,
				"data-composer-stat": stat,
				children: (0, react_jsx_runtime.jsxs)("span", {
					className: StatsPills_module_css_default.pill,
					children: [icon, (0, react_jsx_runtime.jsx)("span", {
						className: StatsPills_module_css_default.label,
						children: label
					})]
				})
			});
		}
		/**
		* A pill button opening its own portaled dialog. Each pill owns its open
		* state; useStatDialog closes it on Escape or an outside pointerdown or click,
		* so at most one dialog is open across the dock.
		*/
		function DialogPill({ stat, icon, label, ariaLabel, title, titleValue, children }) {
			const { open, setOpen, rootRef, panelRef, pos } = useStatDialog();
			return (0, react_jsx_runtime.jsxs)("span", {
				ref: rootRef,
				className: StatsPills_module_css_default.anchor,
				"data-composer-stat": stat,
				children: [(0, react_jsx_runtime.jsxs)("button", {
					type: "button",
					className: StatsPills_module_css_default.pill,
					"aria-haspopup": "dialog",
					"aria-expanded": open,
					"aria-label": ariaLabel,
					onClick: () => {
						setOpen(!open);
					},
					children: [icon, (0, react_jsx_runtime.jsx)("span", {
						className: StatsPills_module_css_default.label,
						children: label
					})]
				}), open && (0, react_dom.createPortal)((0, react_jsx_runtime.jsxs)("div", {
					ref: panelRef,
					className: stat_dialog_module_css_default.panel,
					role: "dialog",
					"aria-label": title,
					style: pos ?? MEASURE_STYLE,
					children: [
						(0, react_jsx_runtime.jsxs)("div", {
							className: stat_dialog_module_css_default.title,
							children: [(0, react_jsx_runtime.jsxs)("span", {
								className: stat_dialog_module_css_default.titleLabel,
								children: [icon, title]
							}), titleValue !== void 0 && (0, react_jsx_runtime.jsx)("span", {
								className: stat_dialog_module_css_default.titleValue,
								children: titleValue
							})]
						}),
						(0, react_jsx_runtime.jsx)("div", {
							className: stat_dialog_module_css_default.titleRule,
							"aria-hidden": true
						}),
						children
					]
				}), document.body)]
			});
		}
		/**
		* Turn and step counts with whole-session speed, opening the time and speed
		* dialog; Compact keeps only the speed reading.
		*/
		const ActivityPill = (0, react.memo)(function ActivityPill({ useChat, useProjection, usePerformanceUsage, t }) {
			const mode = usePerformanceUsage((value) => value);
			const stats = useSessionStats(useChat, useProjection);
			const speed = stats.decodeMs > 0 ? decodeSpeed(stats, t) : null;
			const icon = (0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.IconGaugeOutlineRegular, {});
			if (mode === "compact") return speed === null ? null : (0, react_jsx_runtime.jsx)(PlainPill, {
				stat: "activity",
				icon,
				label: speed
			});
			if (stats.steps === 0) return null;
			const counts = t("stats.counts", {
				turns: stats.turns,
				steps: stats.steps
			});
			const content = {
				stat: "activity",
				icon,
				label: joined$1(counts, speed)
			};
			if (stats.llmMs <= 0 && stats.toolMs <= 0 && stats.ttftSteps <= 0 && speed === null) return (0, react_jsx_runtime.jsx)(PlainPill, { ...content });
			return (0, react_jsx_runtime.jsx)(DialogPill, {
				...content,
				ariaLabel: speed === null ? counts : `${counts} · ${speed}`,
				title: t("stats.dialog.title"),
				children: (0, react_jsx_runtime.jsxs)("dl", {
					className: stat_dialog_module_css_default.details,
					"data-session-stats-details": true,
					children: [
						stats.llmMs > 0 && (0, react_jsx_runtime.jsxs)(react_jsx_runtime.Fragment, { children: [(0, react_jsx_runtime.jsx)("dt", { children: t("stats.dialog.llmTime") }), (0, react_jsx_runtime.jsx)("dd", { children: formatDuration(stats.llmMs, t) })] }),
						stats.toolMs > 0 && (0, react_jsx_runtime.jsxs)(react_jsx_runtime.Fragment, { children: [(0, react_jsx_runtime.jsx)("dt", { children: t("stats.dialog.toolTime") }), (0, react_jsx_runtime.jsx)("dd", { children: formatDuration(stats.toolMs, t) })] }),
						stats.ttftSteps > 0 && (0, react_jsx_runtime.jsxs)(react_jsx_runtime.Fragment, { children: [(0, react_jsx_runtime.jsx)("dt", { children: t("stats.dialog.ttft") }), (0, react_jsx_runtime.jsx)("dd", { children: formatDuration(stats.ttftMs / stats.ttftSteps, t) })] }),
						speed !== null && (0, react_jsx_runtime.jsxs)(react_jsx_runtime.Fragment, { children: [(0, react_jsx_runtime.jsx)("dt", { children: t("stats.dialog.speed") }), (0, react_jsx_runtime.jsx)("dd", { children: speed })] })
					]
				})
			});
		});
		/** Whole-log token total and cache hit; Compact keeps only the cache hit. */
		const UsagePill = (0, react.memo)(function UsagePill({ useProjection, usePerformanceUsage, t }) {
			const mode = usePerformanceUsage((value) => value);
			const usage = useProjection("tokenUsage");
			if (usage === void 0 || billedInputTokens(usage) === 0 && usage.outputTokens === 0) return null;
			const cacheHit = cacheHitPercent(usage);
			const cacheHitText = cacheHit !== null ? t("stats.cacheHit", { percent: cacheHit }) : null;
			const icon = (0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.IconDatabaseOutlineRegular, {});
			if (mode === "compact") return cacheHitText === null ? null : (0, react_jsx_runtime.jsx)(PlainPill, {
				stat: "usage",
				icon,
				label: cacheHitText
			});
			const total = billedInputTokens(usage) + usage.outputTokens;
			const totalText = t("message.turnUsage.count", { count: formatTokens(total, t) });
			return (0, react_jsx_runtime.jsx)(DialogPill, {
				stat: "usage",
				icon,
				label: joined$1(totalText, cacheHitText),
				ariaLabel: cacheHitText === null ? totalText : `${totalText} · ${cacheHitText}`,
				title: t("stats.dialog.usageTitle"),
				titleValue: exactCount(total, t),
				children: (0, react_jsx_runtime.jsxs)("dl", {
					className: stat_dialog_module_css_default.details,
					"data-session-stats-usage": true,
					children: [
						cacheHit !== null && (0, react_jsx_runtime.jsxs)(react_jsx_runtime.Fragment, { children: [(0, react_jsx_runtime.jsx)("dt", { children: t("message.turnUsage.cacheHit") }), (0, react_jsx_runtime.jsx)("dd", { children: `${cacheHit}%` })] }),
						(0, react_jsx_runtime.jsx)("dt", { children: t("message.turnUsage.input") }),
						(0, react_jsx_runtime.jsx)("dd", { children: exactCount(usage.uncachedInputTokens, t) }),
						(0, react_jsx_runtime.jsx)("dt", { children: t("message.turnUsage.cacheRead") }),
						(0, react_jsx_runtime.jsx)("dd", { children: exactCount(usage.cacheReadTokens, t) }),
						usage.cacheWriteTokens !== 0 && (0, react_jsx_runtime.jsxs)(react_jsx_runtime.Fragment, { children: [(0, react_jsx_runtime.jsx)("dt", { children: t("message.turnUsage.cacheWrite") }), (0, react_jsx_runtime.jsx)("dd", { children: exactCount(usage.cacheWriteTokens, t) })] }),
						(0, react_jsx_runtime.jsx)("dt", { children: t("message.turnUsage.output") }),
						(0, react_jsx_runtime.jsx)("dd", { children: exactCount(usage.outputTokens, t) })
					]
				})
			});
		});
export { ActivityPill, UsagePill };