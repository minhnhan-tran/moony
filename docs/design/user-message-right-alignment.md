# User Message Elements Right-Alignment (`user-meta`, `msg-attachments`, `user-body`)

Status: **Approved**

## Context & Motivation

In chat interfaces, user prompt messages and their associated metadata / attachments should consistently dock against the right edge of the conversation view. Previously:
1. `.user-meta` (containing the timestamp, copy prompt button, and retry prompt button) was rendered as a `flex` container with default start/left justification.
2. `.msg-attachments` (containing document chips and image thumbnails) lacked explicit right justification.
3. `.user-body` and `.msg-user` elements inside `.pair-q` and the main transcript needed consistent right-docked positioning with natural left-to-right reading text inside.

## Goals

1. **Right-docked `.user-body`**: User message bubbles dock to the right (`align-self: flex-end; margin-left: auto; width: fit-content; max-width: min(86%, 52rem)`), with natural left-aligned text inside.
2. **Right-aligned `.msg-attachments`**: Attachment row docks to the right (`justify-content: flex-end; align-self: flex-end; margin-left: auto`).
3. **Right-aligned `.user-meta`**: Timestamp and action buttons dock to the right (`justify-content: flex-end; align-self: flex-end; margin-left: auto`).
4. **Consistency**: Works seamlessly in both `.pair-q` pinned headers and the transcript view.

## Implementation Details

### 1. Stylesheet Updates (`internal/web/static/app.css`)
- Configure `.msg-user`, `.user-body`, `.msg-attachments`, and `.user-meta` to consistently align to the right:
  ```css
  .msg-user {
    display: flex;
    flex-direction: column;
    align-items: flex-end;
    width: 100%;
  }
  .user-body {
    align-self: flex-end;
    margin-left: auto;
    width: fit-content;
    max-width: min(86%, 52rem);
    text-align: left;
  }
  .msg-attachments {
    display: flex;
    flex-wrap: wrap;
    justify-content: flex-end;
    align-self: flex-end;
    margin-left: auto;
    width: 100%;
  }
  .user-meta {
    display: flex;
    align-items: center;
    justify-content: flex-end;
    align-self: flex-end;
    margin-left: auto;
    width: 100%;
  }
  ```

### 2. JS Template Classes (`app-render.js`, `app-core.js`)
- Ensure `userMetaFooter` and `renderAttachments` include `justify-end` in their container class lists.

### 3. Verification Plan
- Automated Chrome CDP test (`scripts/verify_user_elements_alignment.mjs`) asserting:
  - `.user-body` right edge aligns with the container right boundary (minus padding).
  - `.msg-attachments` chips and thumbnails align to the right.
  - `.user-meta` clock and action buttons align to the right.
  - Verifies behavior in both standard transcript and `.pair-q` pinned header.
