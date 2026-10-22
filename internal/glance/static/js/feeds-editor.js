import { elem, find } from "./templating.js";

const API_ENDPOINT = pageData.baseURL + "/api/feed-sections";

const sectionsContainer = find("#feeds-editor-sections");
const messageElement = find("#feeds-editor-message");
const saveButton = find("#feeds-editor-save");
const addSectionButton = find("#feeds-editor-add-section");
const importButton = find("#feeds-editor-import");
const importInput = find("#feeds-editor-import-input");

const IMPORT_DEFAULT_SECTION_TITLE = "Imported";
const MAX_TITLE_LENGTH = 100;

let sections = [];
let isDirty = false;
let isSaving = false;

function showMessage(text, isError = false) {
    messageElement.textContent = text;
    messageElement.classList.toggle("feeds-editor-message-error", isError);
}

function markDirty() {
    isDirty = true;
    saveButton.disabled = isSaving;
}

function button(label, title, onClick, extraClass) {
    const b = elem("button");
    b.type = "button";
    b.className = "feeds-editor-icon-button" + (extraClass ? " " + extraClass : "");
    b.textContent = label;
    b.title = title;
    b.setAttribute("aria-label", title);
    b.addEventListener("click", onClick);
    return b;
}

function input(value, placeholder, onInput, extraClass) {
    const i = elem("input");
    i.type = "text";
    i.className = "feeds-editor-input" + (extraClass ? " " + extraClass : "");
    i.value = value;
    i.placeholder = placeholder;
    i.autocomplete = "off";
    i.spellcheck = false;
    i.addEventListener("input", () => { onInput(i.value); markDirty(); });
    return i;
}

function move(list, from, to) {
    if (to < 0 || to >= list.length) return;
    const [item] = list.splice(from, 1);
    list.splice(to, 0, item);
    markDirty();
    render();
}

function renderFeed(section, feed, feedIndex) {
    const row = elem("div");
    row.className = "feeds-editor-feed";

    row.append(
        input(feed.url, "https://example.com/feed.xml", v => feed.url = v, "feeds-editor-input-url"),
        input(feed.title, "Title (optional)", v => feed.title = v, "feeds-editor-input-feed-title"),
        button("✕", "Remove feed", () => {
            section.feeds.splice(feedIndex, 1);
            markDirty();
            render();
        }, "feeds-editor-icon-button-danger"),
    );

    return row;
}

function renderSection(section, sectionIndex) {
    const container = elem("section");
    container.className = "feeds-editor-section widget-content-frame";

    const header = elem("div");
    header.className = "feeds-editor-section-header";

    const titleInput = input(section.title, "Tab name", v => section.title = v, "feeds-editor-input-section-title");
    titleInput.setAttribute("aria-label", "Tab name");

    const upButton = button("↑", "Move tab up", () => move(sections, sectionIndex, sectionIndex - 1));
    const downButton = button("↓", "Move tab down", () => move(sections, sectionIndex, sectionIndex + 1));
    upButton.disabled = sectionIndex == 0;
    downButton.disabled = sectionIndex == sections.length - 1;

    header.append(
        titleInput,
        upButton,
        downButton,
        button("Delete tab", "Delete tab", () => {
            const name = section.title.trim() || "this tab";
            if (!confirm(`Delete "${name}" and all of its feeds?`)) return;
            sections.splice(sectionIndex, 1);
            markDirty();
            render();
        }, "feeds-editor-icon-button-danger feeds-editor-text-button"),
    );

    const feedsList = elem("div");
    feedsList.className = "feeds-editor-feeds";

    if (section.feeds.length == 0) {
        const empty = elem("p");
        empty.className = "color-subdue";
        empty.textContent = "No feeds yet, add one below.";
        feedsList.append(empty);
    }

    section.feeds.forEach((feed, i) => feedsList.append(renderFeed(section, feed, i)));

    const addFeedButton = elem("button");
    addFeedButton.type = "button";
    addFeedButton.className = "feeds-editor-button feeds-editor-add-feed";
    addFeedButton.textContent = "+ Add feed";
    addFeedButton.addEventListener("click", () => {
        section.feeds.push({ url: "", title: "" });
        markDirty();
        render();
        const urlInputs = sectionsContainer.children[sectionIndex].querySelectorAll(".feeds-editor-input-url");
        urlInputs[urlInputs.length - 1]?.focus();
    });

    container.append(header, feedsList, addFeedButton);
    return container;
}

function render() {
    sectionsContainer.replaceChildren(...sections.map(renderSection));
}

async function load() {
    showMessage("Loading…");

    try {
        const response = await fetch(API_ENDPOINT);
        const data = await response.json();

        if (!response.ok) throw new Error(data.error || "Failed to load feeds");

        sections = data.sections;
        isDirty = false;
        saveButton.disabled = true;
        showMessage("");
        render();
    } catch (e) {
        showMessage(e.message, true);
    }
}

async function save() {
    if (isSaving) return;

    isSaving = true;
    saveButton.disabled = true;
    showMessage("Saving…");

    // drop rows that were added but left completely empty
    const payload = {
        sections: sections.map(s => ({
            ...s,
            feeds: s.feeds.filter(f => f.url.trim() != "" || f.title.trim() != ""),
        })),
    };

    try {
        const response = await fetch(API_ENDPOINT, {
            method: "PUT",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(payload),
        });
        const data = await response.json().catch(() => ({}));

        if (!response.ok) throw new Error(data.error || `Failed to save (status ${response.status})`);

        isDirty = false;
        showMessage("Saved. The dashboard is reloading with the new feeds…");

        // the server reloads the config after it changes on disk, wait for that before
        // fetching the sections again so that the indexes of the tabs are up to date
        setTimeout(async () => {
            await load();
            showMessage("Saved.");
        }, 1500);
    } catch (e) {
        showMessage(e.message, true);
        saveButton.disabled = false;
    } finally {
        isSaving = false;
    }
}

function isFeedURL(value) {
    try {
        const url = new URL(value);
        return url.protocol == "http:" || url.protocol == "https:";
    } catch (e) {
        return false;
    }
}

// Returns a list of { section, url, title } where section is null for feeds that
// aren't inside of a category. Nested categories use the innermost category name.
function parseOPML(contents) {
    const doc = new DOMParser().parseFromString(contents, "application/xml");
    const body = doc.getElementsByTagName("body")[0];

    if (doc.getElementsByTagName("parsererror").length > 0 || !body) {
        throw new Error("The file is not a valid OPML file");
    }

    const feeds = [];

    const walk = (parent, section) => {
        for (const outline of parent.children) {
            if (outline.tagName.toLowerCase() != "outline") continue;

            const url = (outline.getAttribute("xmlUrl") || outline.getAttribute("xmlurl") || "").trim();
            const title = (outline.getAttribute("title") || outline.getAttribute("text") || "").trim();

            if (url != "") {
                feeds.push({ section, url, title });
            } else {
                walk(outline, title || section);
            }
        }
    };

    walk(body, null);
    return feeds;
}

function parseTextFile(contents) {
    return contents
        .split(/\r?\n/)
        .map(line => line.trim())
        .filter(line => line != "" && !line.startsWith("#"))
        .map(url => ({ section: null, url, title: "" }));
}

function importFeeds(feeds) {
    let imported = 0;
    let duplicates = 0;
    let invalid = 0;
    const touchedSections = new Set();

    for (const feed of feeds) {
        if (!isFeedURL(feed.url)) {
            invalid++;
            continue;
        }

        const sectionTitle = (feed.section || IMPORT_DEFAULT_SECTION_TITLE).slice(0, MAX_TITLE_LENGTH);
        let section = sections.find(s => s.title.trim().toLowerCase() == sectionTitle.toLowerCase());

        if (!section) {
            section = { index: -1, title: sectionTitle, feeds: [] };
            sections.push(section);
        }

        if (section.feeds.some(f => f.url.trim() == feed.url)) {
            duplicates++;
            continue;
        }

        section.feeds.push({ url: feed.url, title: feed.title.slice(0, MAX_TITLE_LENGTH) });
        touchedSections.add(section);
        imported++;
    }

    return { imported, duplicates, invalid, sectionCount: touchedSections.size };
}

async function handleImportFile(file) {
    try {
        const contents = await file.text();
        const looksLikeXML = /^\s*(<\?xml|<opml)/i.test(contents);
        const feeds = looksLikeXML ? parseOPML(contents) : parseTextFile(contents);

        if (feeds.length == 0) {
            throw new Error("No feeds were found in " + file.name);
        }

        const result = importFeeds(feeds);
        const details = [];
        if (result.duplicates > 0) details.push(`${result.duplicates} already present`);
        if (result.invalid > 0) details.push(`${result.invalid} skipped because of invalid URLs`);

        if (result.imported == 0) {
            showMessage("Nothing new to import" + (details.length ? ` (${details.join(", ")})` : "") + ".", result.invalid > 0);
            return;
        }

        markDirty();
        render();
        showMessage(
            `Imported ${result.imported} feed${result.imported == 1 ? "" : "s"} into ${result.sectionCount} tab${result.sectionCount == 1 ? "" : "s"}` +
            (details.length ? ` (${details.join(", ")})` : "") +
            ". Review them below and click Save changes to apply."
        );
    } catch (e) {
        showMessage(e.message, true);
    }
}

importButton.addEventListener("click", () => importInput.click());

importInput.addEventListener("change", () => {
    const file = importInput.files[0];
    importInput.value = "";
    if (file) handleImportFile(file);
});

addSectionButton.addEventListener("click", () => {
    sections.push({ index: -1, title: "", feeds: [{ url: "", title: "" }] });
    markDirty();
    render();
    const titleInputs = sectionsContainer.querySelectorAll(".feeds-editor-input-section-title");
    titleInputs[titleInputs.length - 1]?.focus();
});

saveButton.addEventListener("click", save);

window.addEventListener("beforeunload", e => {
    if (isDirty) e.preventDefault();
});

load();
