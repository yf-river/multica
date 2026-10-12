"use client";

import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import {
  ChefHat,
  Heart,
  Search,
  ArrowLeft,
  ZoomIn,
  BookOpen,
  Plus,
} from "lucide-react";
import {
  useCookingScroll,
  useCookingList,
  useCookingDetail,
  useCookingCategories,
  useCookingFavorite,
  type CookingEntry,
} from "@multica/core/cooking";
import { ApiError } from "@multica/core/api";
import { useWorkspaceId, useWorkspacePaths } from "@multica/core/paths";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import {
  Dialog,
  DialogContent,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { AppLink, useNavigation, useBackOrReplace } from "../navigation";
import { CollapsedNavTrigger } from "../layout/page-header";
import { RichContent } from "../rich-content/rich-content";
import "./cooking.css";
import { useT } from "../i18n";
import { mealCookingParams } from "@multica/core/meals";

function Shell({
  section,
  children,
  scrollKey,
  ready,
}: {
  section: string;
  children: ReactNode;
  scrollKey?: string;
  ready?: boolean;
}) {
  const { t } = useT("cooking");

  const p = useWorkspacePaths();
  const mealNav = useNavigation();
  const mealContext = mealCookingParams(mealNav.searchParams, p.meals());
  const scrollRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (scrollKey && ready && scrollRef.current)
      scrollRef.current.scrollTop =
        useCookingScroll.getState().positions[scrollKey] ?? 0;
  }, [scrollKey, ready]);

  return (
    <div
      className="cooking h-full overflow-y-auto"
      data-testid="cooking-page"
      ref={scrollRef}
      onScroll={(e) => {
        if (scrollKey && ready)
          useCookingScroll
            .getState()
            .save(scrollKey, e.currentTarget.scrollTop);
      }}
    >
      <div className="mx-auto max-w-[1600px] px-4 py-6 md:px-8 lg:px-12">
        <header className="mb-8">
          <div className="flex items-center gap-3">
            <CollapsedNavTrigger />
            <ChefHat className="cooking-accent" />
            <h1 className="text-title font-semibold">{t(($) => $.title)}</h1>
          </div>
          <nav
            aria-label={t(($) => $.title)}
            className="mt-6 flex gap-8 overflow-x-auto border-b"
          >
            {[
              ["recipes", t(($) => $.recipes), p.cooking()],
              ["tips", t(($) => $.tips), p.cookingTips()],
              ["favorites", t(($) => $.favorites), p.cookingFavorites()],
            ].map(([key, label, href]) => (
              <AppLink
                key={key}
                href={href! + (mealContext.size ? "?" + mealContext : "")}
                aria-current={section === key ? "page" : undefined}
                className={`cooking-tab whitespace-nowrap pb-3 text-body ${section === key ? "active font-semibold" : "text-muted-foreground"}`}
              >
                {label}
              </AppLink>
            ))}
          </nav>
        </header>
        {children}
      </div>
    </div>
  );
}
function Favorite({ entry }: { entry: CookingEntry }) {
  const { t } = useT("cooking");

  const mutation = useCookingFavorite();
  return (
    <div>
      <Button
        variant="ghost"
        size="icon"
        disabled={mutation.isPending}
        aria-label={
          entry.favorite ? `取消收藏${entry.name}` : `收藏${entry.name}`
        }
        aria-pressed={entry.favorite}
        onClick={() =>
          mutation.mutate({ id: entry.id, favorite: !entry.favorite })
        }
      >
        <Heart
          className={entry.favorite ? "fill-current cooking-accent" : ""}
        />
      </Button>
      {mutation.isError && (
        <span role="alert" className="text-caption text-destructive">
          {t(($) => $.action_failed)}
        </span>
      )}
    </div>
  );
}
function Photo({
  src,
  alt,
  className,
}: {
  src: string;
  alt: string;
  className?: string;
}) {
  const { t } = useT("cooking");

  const [failed, setFailed] = useState(false);
  return src && !failed ? (
    <img
      src={src}
      alt={alt}
      loading="lazy"
      decoding="async"
      onError={() => setFailed(true)}
      className={className}
    />
  ) : (
    <div
      className={`flex items-center justify-center bg-muted text-muted-foreground ${className}`}
    >
      <BookOpen aria-hidden="true" />
      <span className="ml-2 text-caption">
        {src ? t(($) => $.image_unavailable) : t(($) => $.text_recipe)}
      </span>
    </div>
  );
}
function Cards({
  items,
  returnTo,
}: {
  items: CookingEntry[];
  returnTo?: string;
}) {
  const { t } = useT("cooking");

  const p = useWorkspacePaths();
  const mealNav = useNavigation();
  const readerParams = mealCookingParams(mealNav.searchParams, p.meals());
  if (returnTo) readerParams.set("from", returnTo);
  return (
    <div className="grid grid-cols-1 gap-6 sm:grid-cols-2 lg:grid-cols-3 2xl:grid-cols-4">
      {items.map((e) => {
        const href =
          e.kind === "tip" ? p.cookingTip(e.id) : p.cookingRecipes(e.id);
        return (
          <article
            key={e.id}
            className="overflow-hidden rounded-xl border bg-card transition-shadow hover:shadow-md"
          >
            {e.available ? (
              <AppLink
                href={
                  href +
                  (readerParams.size ? "?" + readerParams : "")
                }
                className="block"
              >
                <Photo
                  src={e.cover}
                  alt={e.name}
                  className="aspect-[8/5] w-full object-cover"
                />
              </AppLink>
            ) : (
              <div className="aspect-[8/5] bg-muted p-6">
                {t(($) => $.unavailable)}
              </div>
            )}
            <div className="p-4">
              <div className="flex items-start justify-between gap-2">
                {e.available ? (
                  <AppLink
                    href={
                      href +
                      (readerParams.size ? "?" + readerParams : "")
                    }
                    className="text-title-sm font-semibold"
                  >
                    {e.name}
                  </AppLink>
                ) : (
                  <h2>{e.name}</h2>
                )}
                {e.kind === "recipe" && <Favorite entry={e} />}
              </div>
              <div className="mt-2 flex flex-wrap gap-3 text-caption text-muted-foreground">
                <span>{e.categoryName}</span>
                {e.difficulty && <span>{e.difficulty}</span>}
                {e.illustrations.length > 0 && (
                  <span className="cooking-accent">
                    {t(($) => $.illustrated)}
                  </span>
                )}
              </div>
            </div>
          </article>
        );
      })}
    </div>
  );
}
function ErrorState({ retry }: { retry: () => void }) {
  const { t } = useT("cooking");

  return (
    <div role="alert" className="py-16 text-center">
      <p>{t(($) => $.load_failed)}</p>
      <Button variant="outline" className="mt-4" onClick={retry}>
        {t(($) => $.retry)}
      </Button>
    </div>
  );
}
export function CookingPage({
  section = "recipes",
}: {
  section?: "recipes" | "tips" | "favorites";
}) {
  const { t } = useT("cooking");

  const wsId = useWorkspaceId(),
    p = useWorkspacePaths(),
    nav = useNavigation();
  const listParams = new URLSearchParams();
  for (const key of ["q", "category", "page"]) {
    const value = nav.searchParams.get(key);
    if (value) listParams.set(key, value);
  }
  const query = listParams.toString();
  const [search, setSearch] = useState(nav.searchParams.get("q") ?? "");
  const categories = useCookingCategories(wsId);
  const list = useCookingList(wsId, section, query);
  const update = useCallback(
    (changes: Record<string, string>) => {
      const params = new URLSearchParams(query);
      mealCookingParams(nav.searchParams, p.meals()).forEach((value, key) => params.set(key, value));
      for (const [key, value] of Object.entries(changes)) {
        if (value) params.set(key, value);
        else params.delete(key);
      }
      if (!("page" in changes)) params.delete("page");
      nav.replace(nav.pathname + (params.size ? `?${params}` : ""));
    },
    [query, nav],
  );
  const currentQuery = nav.searchParams.get("q") ?? "";
  useEffect(() => setSearch(currentQuery), [currentQuery]);
  useEffect(() => {
    if (search === currentQuery) return;
    const timer = setTimeout(() => update({ q: search }), 350);
    return () => clearTimeout(timer);
  }, [search, currentQuery, update]); // URL is the durable filter state.
  const category = nav.searchParams.get("category") ?? "";
  const page = list.data?.page ?? 1;
  const items = list.data?.items ?? [];
  const preferredCategoryIds = [
    "vegetable_dish",
    "meat_dish",
    "aquatic",
    "breakfast",
    "staple",
    "soup",
  ];
  const cats = categories.data ?? [];
  const orderedCategories = [
    ...preferredCategoryIds.flatMap((id) => cats.filter((c) => c.id === id)),
    ...cats.filter((c) => !preferredCategoryIds.includes(c.id)),
  ];
  return (
    <Shell
      section={section}
      scrollKey={nav.pathname + "?" + query}
      ready={!!list.data}
    >
      <div className="mb-8 max-w-2xl">
        {section !== "recipes" && (
          <h2 className="mb-4 text-title font-semibold">
            {section === "favorites"
              ? t(($) => $.favorites_heading)
              : t(($) => $.tips_heading)}
          </h2>
        )}
        <form
          onSubmit={(e) => {
            e.preventDefault();
            update({ q: search });
          }}
          className="relative"
        >
          <Search className="absolute left-3 top-3 size-4 text-muted-foreground" />
          <Input
            aria-label={
              section === "tips"
                ? t(($) => $.search_tips)
                : t(($) => $.search_recipes)
            }
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder={
              section === "tips"
                ? t(($) => $.search_tips)
                : t(($) => $.search_recipes)
            }
            className="h-11 pl-10"
          />
        </form>
      </div>
      {section !== "tips" && (
        <div className="mb-6 flex flex-wrap gap-2">
          <Button
            variant={!category ? "secondary" : "ghost"}
            aria-pressed={!category}
            onClick={() => update({ category: "" })}
          >
            {t(($) => $.all)}
          </Button>
          {orderedCategories.map((c) => (
            <Button
              key={c.id}
              variant={category === c.id ? "secondary" : "ghost"}
              aria-pressed={category === c.id}
              onClick={() => update({ category: c.id })}
            >
              {c.name}
            </Button>
          ))}
        </div>
      )}
      <div className="mb-6">
        <h2 className="text-title-sm font-semibold">
          {section === "favorites"
            ? t(($) => $.favorites)
            : section === "tips"
              ? t(($) => $.tips)
              : t(($) => $.all_recipes)}
          <span className="ml-3 text-body font-normal text-muted-foreground">
            {list.data?.total ?? "—"} {t(($) => $.count_unit)}
          </span>
        </h2>

      </div>
      {list.isPending ? (
        <p role="status" className="py-20 text-center text-muted-foreground">
          {t(($) => $.loading)}
        </p>
      ) : list.isError || !list.data ? (
        <ErrorState retry={() => void list.refetch()} />
      ) : items.length === 0 ? (
        <div className="py-20 text-center">
          <ChefHat className="mx-auto mb-4 size-10 text-muted-foreground" />
          <p>
            {section === "favorites" && !query
              ? t(($) => $.empty_favorites)
              : t(($) => $.empty_results)}
          </p>
          <AppLink
            href={p.cooking()}
            className="mt-4 inline-block cooking-accent"
          >
            {t(($) => $.browse)}
          </AppLink>
        </div>
      ) : section === "tips" ? (
        <TipsBrowser items={items} />
      ) : (
        <Cards
          items={items}
          returnTo={nav.pathname + (query ? `?${query}` : "")}
        />
      )}
      {list.data && list.data.total > 24 && (
        <nav
          aria-label={t(($) => $.pagination)}
          className="mt-10 flex items-center justify-center gap-4"
        >
          <Button
            variant="outline"
            disabled={page <= 1}
            onClick={() => update({ page: String(page - 1) })}
          >
            {t(($) => $.previous)}
          </Button>
          <span>
            {page} / {Math.ceil(list.data.total / 24)}
          </span>
          <Button
            variant="outline"
            disabled={page * 24 >= list.data.total}
            onClick={() => update({ page: String(page + 1) })}
          >
            {t(($) => $.next)}
          </Button>
        </nav>
      )}
    </Shell>
  );
}
function ImageViewer({ src, onClose }: { src: string; onClose: () => void }) {
  const { t } = useT("cooking");

  const [scale, setScale] = useState(1);
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <DialogContent className="!max-w-[95vw] !w-[1200px] max-h-[95dvh] overflow-hidden">
        <DialogTitle>{t(($) => $.image_viewer)}</DialogTitle>
        <div className="flex gap-3">
          <Button
            variant="outline"
            aria-label={t(($) => $.zoom_out)}
            onClick={() => setScale(Math.max(0.5, scale - 0.25))}
          >
            −
          </Button>
          <Button variant="outline" onClick={() => setScale(1)}>
            {t(($) => $.fit)}
          </Button>
          <Button
            variant="outline"
            aria-label={t(($) => $.zoom_in)}
            onClick={() => setScale(Math.min(4, scale + 0.25))}
          >
            <Plus className="size-4" />
          </Button>
          <span className="self-center text-caption">
            {Math.round(scale * 100)}%
          </span>
        </div>
        <div
          className="max-h-[70dvh] overflow-auto"
          style={{ touchAction: "pan-x pan-y pinch-zoom" }}
        >
          <img
            src={src}
            alt={t(($) => $.full_image)}
            style={{ width: `${scale * 100}%`, maxWidth: "none" }}
          />
        </div>
      </DialogContent>
    </Dialog>
  );
}
function TipsDirectory({ items, id }: { items: CookingEntry[]; id: string }) {
  const { t } = useT("cooking");
  const p = useWorkspacePaths(),
    nav = useNavigation();
  return (
    <aside>
      <select
        className="w-full rounded border bg-background p-2 lg:hidden"
        aria-label={t(($) => $.select_tip)}
        value={id}
        onChange={(e) => nav.push(p.cookingTip(e.target.value))}
      >
        {items.map((t) => (
          <option key={t.id} value={t.id}>
            {t.name}
          </option>
        ))}
      </select>
      <nav
        aria-label={t(($) => $.tips_directory)}
        className="hidden space-y-3 lg:block"
      >
        {Array.from(new Set(items.map((t) => t.categoryName))).map((group) => (
          <div key={group}>
            <h3 className="mb-2 font-medium">{group}</h3>
            {items
              .filter((t) => t.categoryName === group)
              .map((t) => (
                <AppLink
                  className={`block py-1 text-caption ${t.id === id ? "cooking-accent font-semibold" : "text-muted-foreground"}`}
                  key={t.id}
                  href={p.cookingTip(t.id)}
                >
                  {t.name}
                </AppLink>
              ))}
          </div>
        ))}
      </nav>
    </aside>
  );
}
function TipsBrowser({ items }: { items: CookingEntry[] }) {
  const wsId = useWorkspaceId();
  const selected =
    items.find((e) => e.sourceUrl.includes(encodeURIComponent("厨房准备"))) ??
    items[0]!;
  const detail = useCookingDetail(wsId, "tips", selected.id);
  return (
    <div className="grid gap-8 lg:grid-cols-[200px_minmax(0,1fr)]">
      <TipsDirectory items={items} id={selected.id} />
      <div className="min-w-0">
        {detail.isError ? (
          <ErrorState retry={() => void detail.refetch()} />
        ) : (
          detail.data?.entry && (
            <Reading key={detail.data.entry.id} entry={detail.data.entry} />
          )
        )}
      </div>
    </div>
  );
}

function Reading({ entry }: { entry: CookingEntry }) {
  const { t } = useT("cooking");

  const p = useWorkspacePaths();
  const ref = useRef<HTMLDivElement>(null);
  const [image, setImage] = useState("");
  const markdown = entry.markdown.replace(
    /\/__cooking\/(recipes|tips)\/([a-f0-9]+)/g,
    (_, kind: string, id: string) =>
      kind === "tips" ? p.cookingTip(id) : p.cookingRecipes(id),
  );
  useEffect(() => {
    const root = ref.current;
    if (!root) return;
    // Preparation progress is ephemeral DOM state; the canonical markdown stays intact.
    root.querySelectorAll("h2").forEach((h) => {
      if (!/原料|材料|工具/.test(h.textContent ?? "")) return;
      let node = h.nextElementSibling;
      while (node && !/^H[12]$/.test(node.tagName)) {
        node.querySelectorAll("li").forEach((li) => {
          if (li.querySelector("input")) return;
          const checkbox = document.createElement("input");
          checkbox.type = "checkbox";
          checkbox.setAttribute("aria-label", `准备${li.textContent}`);
          checkbox.className = "mr-2";
          li.prepend(checkbox);
        });
        node = node.nextElementSibling;
      }
    });
  }, [markdown]);
  const hasImages = entry.illustrations.length > 0,
    hasText = !!entry.markdown;
  return (
    <>
      <div
        className={
          hasImages && hasText
            ? "grid min-w-0 gap-8 md:grid-cols-2"
            : "min-w-0"
        }
      >
        {hasImages && (
          <div className="min-w-0 space-y-4">
            {entry.illustrations.map((src) => (
              <div key={src}>
                <button
                  onClick={() => setImage(src)}
                  aria-label={`放大${entry.name}图解`}
                  className="block w-full overflow-hidden rounded-xl border"
                >
                  <Photo
                    src={src}
                    alt={`${entry.name}图解`}
                    className="w-full"
                  />
                </button>
                <Button variant="ghost" onClick={() => setImage(src)}>
                  <ZoomIn className="size-4" />
                  {t(($) => $.enlarge)}
                </Button>
              </div>
            ))}
          </div>
        )}
        {hasText && (
          <div className="min-w-0">
            <div ref={ref}>
              <RichContent content={markdown} density="document" />
            </div>
          </div>
        )}
      </div>
      {image && <ImageViewer src={image} onClose={() => setImage("")} />}
    </>
  );
}
export function CookingDetailPage({
  id,
  kind = "recipes",
}: {
  id: string;
  kind?: "recipes" | "tips";
}) {
  const { t } = useT("cooking");

  const wsId = useWorkspaceId(),
    p = useWorkspacePaths(),
    nav = useNavigation(),
    back = useBackOrReplace();
  const detail = useCookingDetail(wsId, kind, id);
  const tips = useCookingList(wsId, "tips", "");
  const entry = detail.data?.entry;
  const mealContext = mealCookingParams(nav.searchParams, p.meals());
  const mealReturn = mealContext.get("mealReturn");
  const mealLabels = useT("meals").t;
  const arrangeParams = new URLSearchParams(mealReturn?.split("?")[1] || "");
  arrangeParams.set("recipe", id);
  arrangeParams.set("date", mealContext.get("mealDate") || "");
  arrangeParams.set("slot", mealContext.get("mealSlot") || "lunch");
  const from = nav.searchParams.get("from");
  const fallback = from?.startsWith(p.cooking())
    ? from
    : kind === "tips"
      ? p.cookingTips()
      : p.cooking();
  return (
    <Shell section={kind === "tips" ? "tips" : "recipes"}>
      {mealReturn && <div className="mb-4 flex flex-wrap items-center gap-3"><AppLink className="inline-flex min-h-11 items-center rounded-md border px-4 text-body hover:bg-accent" href={mealReturn}>{mealLabels(($) => $.title)} ←</AppLink>{kind === "recipes" && <AppLink className="inline-flex min-h-11 items-center rounded-md bg-primary px-4 text-body text-primary-foreground" href={p.meals() + "?" + arrangeParams}>{mealLabels(($) => $.add)}</AppLink>}</div>}
      <Button variant="ghost" className="mb-4" onClick={() => back(fallback)}>
        <ArrowLeft className="size-4" />
        {t(($) => $.back)}
        {kind === "tips" ? t(($) => $.tips) : t(($) => $.recipe)}
      </Button>
      {detail.isPending ? (
        <p role="status">{t(($) => $.reading)}</p>
      ) : detail.error instanceof ApiError && detail.error.status === 404 ? (
        <p role="alert" className="py-16 text-center">
          {t(($) => $.not_found)}
        </p>
      ) : detail.isError || !entry ? (
        <ErrorState retry={() => void detail.refetch()} />
      ) : (
        <>
          <header className="mb-6 flex items-start justify-between gap-4">
            <div>
              <h2 className="text-title font-semibold">{entry.name}</h2>
              <p className="mt-2 text-caption text-muted-foreground">
                {entry.categoryName} {entry.difficulty}
              </p>
            </div>
            {kind === "recipes" && <Favorite entry={entry} />}
          </header>
          <div
            className={
              kind === "tips"
                ? "grid gap-8 lg:grid-cols-[200px_minmax(0,1fr)]"
                : ""
            }
          >
            {kind === "tips" && (
              <TipsDirectory items={tips.data?.items ?? []} id={id} />
            )}
            <Reading key={entry.id} entry={entry} />
          </div>
          {kind === "recipes" && !!detail.data?.related.length && (
            <section className="mt-8">
              <h3 className="mb-5 text-title-sm font-semibold">
                {t(($) => $.related)}
              </h3>
              <Cards items={detail.data.related} />
            </section>
          )}
        </>
      )}
    </Shell>
  );
}
