// 实时末端"跟随"锁存状态机，供倒序（最新在前）转录（#5921）与底部锚定的聊天列表
// （chat/components/stick-to-bottom.ts）共用。
//
// 两个界面的共同难题：系统会自己挪动视口（转录里是最前端的锚定插入；聊天列表里是
// 流的增长、输入框尺寸变化与随之而来的滚动夹紧），所以"我现在还在实时末端吗"无法
// 区分"读者自己滚走了"和"视口被系统顶走了"。本锁存保留这个区分：
//
// - 仅凭输入永远不解锁。输入只是为当前渲染帧开一笔"认领额度"，只有界面自己的滚动
//   能确认这是读者驱动的位移。接线层在那一帧之后丢弃没被花掉的认领，避免后续系统
//   位移继承到嵌套滚动区消费过的输入。
// - 被确认的位移用界面的位移量，而非原始输入量。稳定窗口内同方向连续运动都算读者
//   滚动，含触屏惯性滚动与亚像素拖拽帧。
// - 归因给读者的位移会累计到读者回到末端为止；它不随手势结束而过期，被钉回也不会
//   清零。触控板甩一下和阅读速度下五次独立的滚轮齿跨越同一阈值，对快流的反复
//   亚阈值尝试最终会积累成一次解锁，而不是永远被重新钉住。
// - 跟随中，非读者归因的位移直接钉回实时末端（`onScroll`/`onResize` 返回裁定），
//   立即执行、没有给末尾事件藏身的意图窗口——但触屏活动或鼠标按住期间绝不钉
//   （不能和选中文本的自动滚动打架）。
// - 读者回到边缘区内时重新进入跟随。
//
// 状态机不感知方向：调用方喂给它"远离末端为正"的输入增量和当前距各自末端的距离
// （倒序转录用 scrollTop，聊天列表用距底部距离）。
//
// 纯状态机使判定表可以单测；每个界面各自负责把它接到 DOM 事件上。

// 宽容的"位于实时末端"区：距实时边缘这个距离以内都算读者仍在跟随。聊天列表也把
// 它作为 Virtuoso 的 `atBottomThreshold`，让两处对"到底部了没"的判定一致。
export const FOLLOW_EDGE_THRESHOLD = 120;

// 一次输入与确认它的滚动之间（或已确认运动中的两次滚动之间）允许的最大间隔。
// DOM 接线层通常在下一动画帧后就会清掉未确认的输入，不会等满这个窗口。
const MOTION_SETTLE_WINDOW_MS = 300;

// WheelEvent.deltaMode 为 1（行）/ 2（页）时的换算基准。
export const LINE_SCROLL_PX = 40;

export interface LiveEndFollow {
  /** 界面是否处于直播状态；未激活时所有行为都是惰性的。 */
  setActive(active: boolean): void;
  /** 列表换成新实例（换任务/排序/筛选）：回到跟随状态。 */
  reset(): void;
  isFollowing(): boolean;
  /** 显式离开实时末端（如点了某个分段）。 */
  disengage(): void;
  /**
   * 用户的滚动输入，单位 px；正数 = 远离末端。为界面自己的滚动开一笔可归因的
   * 额度预算。
   */
  input(delta: number): void;
  /** 丢弃界面在渲染帧内没消费掉的输入。 */
  endInputFrame(): void;
  /** 触屏开始——拖动位移被确认期间不允许钉回。 */
  touchStart(): void;
  /** 触屏结束；稳定窗口内确认过的惯性可以继续。 */
  touchEnd(): void;
  /** 滚动区内部按下鼠标；`onScroller` 为真表示按在元素自身（滚动条）上。 */
  pointerDown(onScroller: boolean): void;
  pointerUp(): void;
  onAtEdgeChange(atEdge: boolean): void;
  /**
   * 界面自己滚动了。用来确认暂存的输入或延续最近的读者运动；返回是否要把视口
   * 钉回实时末端。
   */
  onScroll(distance: number): boolean;
  /**
   * 实时末端在没有滚动的情况下动了（内容增长、视口缩放）。从不把位移归因给读者；
   * 返回是否要钉回。
   */
  onResize(distance: number): boolean;
}

export function createLiveEndFollow(now: () => number = () => Date.now()): LiveEndFollow {
  let active = false;
  let following = true;
  // 本帧未花掉的输入认领，按方向分开记。认领只有在界面确认了同向位移后才变成
  // "读者运动"。
  let awayBudget = 0;
  let towardBudget = 0;
  let lastInputAt = -MOTION_SETTLE_WINDOW_MS;
  // 读者真正产生的位移，一格一格滚动确认出来的。不过期、不被钉回清零：对快流的
  // 反复亚阈值尝试会积累成一次解锁，而不是每轮都输给重新钉回。
  let awayTaken = 0;
  let lastDistance = 0;
  let mouseHeld = false;
  let scrollbarDrag = false;
  let touchHeld = false;
  let motionDirection: -1 | 0 | 1 = 0;
  let lastMotionAt = -MOTION_SETTLE_WINDOW_MS;
  // 当前这段运动的来源，以及它背后那笔认领还剩多少没花。触屏的惯性滑行可以超出
  // 认领额度（惯性距离跟手指行程不成比例）；其他来源只能在读者真正要求的范围内
  // 继续，所以窗口期内落到系统位移（如转录追加）时没有额度可花，只能落到钉回。
  let motionSource: "touch" | "input" | null = null;
  let motionCarry = 0;

  const inputFresh = () => now() - lastInputAt < MOTION_SETTLE_WINDOW_MS;
  const motionFresh = () => now() - lastMotionAt < MOTION_SETTLE_WINDOW_MS;

  // 剩余额度按小数步消费，累计总和会与界面自己的运算差出几个 ulp。相对手中额度
  // 这个量级的差距只是舍入误差——而系统挪视口是以像素计的。
  const withinCarry = (amount: number) =>
    amount <= motionCarry + Math.max(1, motionCarry) * 1e-9;

  const pinVerdict = (distance: number): boolean =>
    following && !mouseHeld && !scrollbarDrag && !touchHeld && distance > 0;

  return {
    setActive(a: boolean) {
      active = a;
    },
    reset() {
      following = true;
      awayBudget = 0;
      towardBudget = 0;
      awayTaken = 0;
      lastDistance = 0;
      mouseHeld = false;
      scrollbarDrag = false;
      touchHeld = false;
      motionDirection = 0;
      motionSource = null;
      motionCarry = 0;
      lastMotionAt = -MOTION_SETTLE_WINDOW_MS;
    },
    isFollowing: () => active && following,
    disengage() {
      if (active) following = false;
    },
    input(delta: number) {
      if (!active) return;
      // 新手势开新预算：界面从未兑现过的认领不能残留进后续手势。
      if (!inputFresh()) {
        awayBudget = 0;
        towardBudget = 0;
      }
      lastInputAt = now();
      if (delta > 0) awayBudget += delta;
      else towardBudget -= delta;
    },
    endInputFrame() {
      awayBudget = 0;
      towardBudget = 0;
    },
    touchStart() {
      if (active) touchHeld = true;
    },
    touchEnd() {
      touchHeld = false;
    },
    pointerDown(onScroller: boolean) {
      if (!active) return;
      mouseHeld = true;
      if (onScroller) scrollbarDrag = true;
    },
    pointerUp() {
      mouseHeld = false;
      scrollbarDrag = false;
    },
    onAtEdgeChange(atEdge: boolean) {
      if (!active || !atEdge) return;
      following = true;
      awayBudget = 0;
      towardBudget = 0;
      awayTaken = 0;
      motionDirection = 0;
      motionSource = null;
      motionCarry = 0;
    },
    onScroll(distance: number): boolean {
      if (!active) return false;
      const moved = distance - lastDistance;
      lastDistance = distance;
      // 拖滚动条完全由用户控制：绝对位置就是用户的位移，直接用普通阈值判定。
      if (scrollbarDrag) {
        if (distance > FOLLOW_EDGE_THRESHOLD) following = false;
        return false;
      }
      // 该方向已确认过的运动可继续，无需新认领。触屏可自由滑出额度（惯性距离与
      // 手指行程不成比例）；其余来源受界面未花完的认领上限约束，窗口期落进来的
      // 系统位移（转录前置追加）找不到可花的额度，直接落入钉回分支。
      if (moved > 0 && motionDirection === 1 && (touchHeld || motionFresh())) {
        // 动画中途到达的一格滚轮齿由这次同向滚动确认，把它并入：否则结束认领的
        // 那一帧会把它丢弃，并把读者剩下的滚动一起钉回去。
        if (inputFresh() && awayBudget > 0) {
          motionCarry += awayBudget;
          awayBudget = 0;
        }
        if (motionSource === "touch" || withinCarry(moved)) {
          if (motionSource !== "touch") motionCarry = Math.max(0, motionCarry - moved);
          awayTaken += moved;
          lastMotionAt = now();
          if (following && awayTaken > FOLLOW_EDGE_THRESHOLD) following = false;
          return false;
        }
      } else if (moved < 0 && motionDirection === -1 && (touchHeld || motionFresh())) {
        if (inputFresh() && towardBudget > 0) {
          motionCarry += towardBudget;
          towardBudget = 0;
        }
        if (motionSource === "touch" || withinCarry(-moved)) {
          if (motionSource !== "touch") motionCarry = Math.max(0, motionCarry + moved);
          awayTaken = Math.max(0, awayTaken + moved);
          lastMotionAt = now();
          if (distance === 0) {
            following = true;
            awayBudget = 0;
            towardBudget = 0;
            awayTaken = 0;
            motionDirection = 0;
            motionSource = null;
            motionCarry = 0;
          }
          return false;
        }
      }
      if (moved > 0 && inputFresh() && awayBudget > 0) {
        const attributed = Math.min(awayBudget, moved);
        awayBudget -= attributed;
        // 触屏位移不必与手指行程逐像素吻合。
        if (touchHeld || attributed === moved) {
          awayTaken += moved;
          motionDirection = 1;
          motionSource = touchHeld ? "touch" : "input";
          // 一格滚轮齿可能跨多帧动画；接线层在第一帧后就丢弃认领，所以把剩余
          // 额度转进 motionCarry 让运动继续。
          motionCarry = awayBudget;
          lastMotionAt = now();
          if (following && awayTaken > FOLLOW_EDGE_THRESHOLD) following = false;
          return false;
        }
      } else if (moved < 0 && inputFresh() && towardBudget > 0) {
        const attributed = Math.min(towardBudget, -moved);
        towardBudget -= attributed;
        if (touchHeld || attributed === -moved) {
          awayTaken = Math.max(0, awayTaken + moved);
          motionDirection = -1;
          motionSource = touchHeld ? "touch" : "input";
          motionCarry = towardBudget;
          lastMotionAt = now();
          if (distance === 0) {
            following = true;
            awayBudget = 0;
            towardBudget = 0;
            awayTaken = 0;
            motionDirection = 0;
          }
          return false;
        }
      }
      return pinVerdict(distance);
    },
    onResize(distance: number): boolean {
      if (!active) return false;
      if (pinVerdict(distance)) {
        // 调用方钉回末端；记录目的位置，让钉回自身的滚动（或测试里根本没有滚动）
        // 被读成"没有位移"。
        lastDistance = 0;
        return true;
      }
      lastDistance = distance;
      return false;
    },
  };
}
