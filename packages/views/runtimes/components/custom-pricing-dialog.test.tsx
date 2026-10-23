// @vitest-environment jsdom

import { cleanup, fireEvent, screen } from "@testing-library/react";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { useCustomPricingStore } from "@multica/core/runtimes/custom-pricing-store";
import { renderWithI18n } from "../../test/i18n";
import { CustomPricingDialog } from "./custom-pricing-dialog";

const MODEL = "acme/delete-fixture";
const PRICE = { input: 7, output: 10, cacheRead: 1, cacheWrite: 7 };

function Fixture() {
  const [open, setOpen] = useState(true);
  return <>
    <button onClick={() => setOpen(true)}>Reopen prices</button>
    <CustomPricingDialog open={open} onOpenChange={setOpen} unmappedModels={[MODEL]} />
  </>;
}

beforeEach(() => {
  useCustomPricingStore.setState({ pricings: { [MODEL]: PRICE } });
});

afterEach(() => {
  cleanup();
  useCustomPricingStore.setState({ pricings: {} });
});

describe("CustomPricingDialog deletion", () => {
  it("does not restore a deleted override when saving a retained unmapped row", () => {
    renderWithI18n(<Fixture />);
    fireEvent.change(screen.getAllByRole("spinbutton")[0]!, { target: { value: "99" } });
    fireEvent.click(screen.getByRole("button", { name: "Remove custom price" }));
    expect(useCustomPricingStore.getState().pricings[MODEL]).toBeUndefined();
    fireEvent.click(screen.getByRole("button", { name: "Save", exact: true }));
    expect(useCustomPricingStore.getState().pricings[MODEL]).toBeUndefined();
    fireEvent.click(screen.getByRole("button", { name: "Reopen prices" }));
    expect(screen.queryByRole("button", { name: "Remove custom price" })).toBeNull();
  });

  it("keeps immediate deletion when cancelling the dialog", () => {
    renderWithI18n(<Fixture />);
    fireEvent.click(screen.getByRole("button", { name: "Remove custom price" }));
    fireEvent.click(screen.getByRole("button", { name: "Cancel", exact: true }));
    expect(useCustomPricingStore.getState().pricings[MODEL]).toBeUndefined();
  });

  it("discards an unsaved edit on cancel and saves an explicit new price after deletion", () => {
    renderWithI18n(<Fixture />);
    fireEvent.change(screen.getAllByRole("spinbutton")[0]!, { target: { value: "99" } });
    fireEvent.click(screen.getByRole("button", { name: "Cancel", exact: true }));
    expect(useCustomPricingStore.getState().pricings[MODEL]).toEqual(PRICE);
    fireEvent.click(screen.getByRole("button", { name: "Reopen prices" }));
    fireEvent.click(screen.getByRole("button", { name: "Remove custom price" }));
    for (const input of screen.getAllByRole("spinbutton")) {
      fireEvent.change(input, { target: { value: "2" } });
    }
    fireEvent.click(screen.getByRole("button", { name: "Save", exact: true }));
    expect(useCustomPricingStore.getState().pricings[MODEL]).toEqual({
      input: 2, output: 2, cacheRead: 2, cacheWrite: 2,
    });
  });
});
