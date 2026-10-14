import { render, fireEvent } from "@testing-library/react";
import { describe, it, expect, vi } from "vitest";
import { ChatAddMenu } from "./chat-add-menu";
vi.mock("../../i18n", () => ({ useT: () => ({ t: () => "Upload files" }) }));

describe("chat batch file picker", () => {
  it("accepts all selected résumés and resets so the same batch can be picked again", () => {
    const onSelectFile = vi.fn();
    const { container } = render(<ChatAddMenu onSelectFile={onSelectFile} />);
    const input = container.querySelector<HTMLInputElement>('input[type="file"]')!;
    expect(input.multiple).toBe(true);
    const files = ["first.pdf", "second.docx", "third.pdf"].map((name) => new File(["resume"], name));
    fireEvent.change(input, { target: { files } });
    expect(onSelectFile.mock.calls.map(([file]) => file.name)).toEqual(files.map((file) => file.name));
    expect(input.value).toBe("");
    fireEvent.change(input, { target: { files } });
    expect(onSelectFile).toHaveBeenCalledTimes(6);
  });
  it("does not upload when the user cancels the picker", () => {
    const onSelectFile = vi.fn();
    const { container } = render(<ChatAddMenu onSelectFile={onSelectFile} />);
    fireEvent.change(container.querySelector('input[type="file"]')!, { target: { files: [] } });
    expect(onSelectFile).not.toHaveBeenCalled();
  });
});
