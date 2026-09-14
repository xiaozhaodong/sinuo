import test from 'node:test';
import assert from 'node:assert/strict';
import { createModalLifecycleManager } from './modalLifecycle.js';

class FakeElement {
  constructor(documentTarget) {
    this.documentTarget = documentTarget;
    this.connected = true;
    this.children = [];
    this.tabIndex = 0;
    this.disabled = false;
    this.visible = true;
    this.inert = false;
  }

  focus() {
    this.documentTarget.activeElement = this;
    this.documentTarget.dispatch('focusin', { target: this });
  }

  querySelectorAll() {
    return this.children;
  }

  contains(element) {
    return element === this || this.children.includes(element);
  }

  closest() {
    return this.inert ? this : null;
  }

  getClientRects() {
    return this.visible ? [{}] : [];
  }
}

class FakeDocument {
  constructor() {
    this.listeners = new Map();
    this.activeElement = null;
    this.scrollTarget = {
      dataset: {},
      style: { overflow: '' }
    };
    this.body = this.scrollTarget;
  }

  addEventListener(type, listener) {
    const listeners = this.listeners.get(type) || [];
    listeners.push(listener);
    this.listeners.set(type, listeners);
  }

  removeEventListener(type, listener) {
    const listeners = this.listeners.get(type) || [];
    this.listeners.set(type, listeners.filter((candidate) => candidate !== listener));
  }

  dispatch(type, event) {
    for (const listener of [...(this.listeners.get(type) || [])]) listener(event);
  }

  dispatchKey(key, shiftKey = false) {
    const event = { key, shiftKey, defaultPrevented: false, preventDefault() { this.defaultPrevented = true; } };
    this.dispatch('keydown', event);
    return event;
  }

  getElementById(id) {
    return id === 'app-scroll-container' ? this.scrollTarget : null;
  }

  contains(element) {
    return element?.connected === true;
  }

  createElement() {
    return new FakeElement(this);
  }
}

const withFakeDocument = (run) => {
  const previousDocument = globalThis.document;
  const documentTarget = new FakeDocument();
  globalThis.document = documentTarget;
  try {
    return run(documentTarget);
  } finally {
    globalThis.document = previousDocument;
  }
};

test('one Escape closes only the topmost modal', () => withFakeDocument((documentTarget) => {
  const manager = createModalLifecycleManager();
  let lowerCloseCount = 0;
  let upperCloseCount = 0;

  const cleanupLower = manager.activate({
    getOnClose: () => () => { lowerCloseCount += 1; },
    initialFocusElement: documentTarget.createElement()
  });
  let cleanupUpper = () => {};
  cleanupUpper = manager.activate({
    getOnClose: () => () => {
      upperCloseCount += 1;
      cleanupUpper();
    },
    initialFocusElement: documentTarget.createElement()
  });

  documentTarget.dispatchKey('Escape');

  assert.equal(upperCloseCount, 1);
  assert.equal(lowerCloseCount, 0);

  cleanupLower();
}));

test('a topmost modal that refuses to close still shields lower modals', () => withFakeDocument((documentTarget) => {
  const manager = createModalLifecycleManager();
  let lowerCloseCount = 0;
  let upperCloseCount = 0;

  const cleanupLower = manager.activate({
    getOnClose: () => () => { lowerCloseCount += 1; },
    initialFocusElement: documentTarget.createElement()
  });
  const cleanupUpper = manager.activate({
    getOnClose: () => () => { upperCloseCount += 1; },
    initialFocusElement: documentTarget.createElement()
  });

  documentTarget.dispatchKey('Escape');

  assert.equal(upperCloseCount, 1);
  assert.equal(lowerCloseCount, 0);

  cleanupUpper();
  cleanupLower();
}));

test('removing a lower modal out of order keeps focus and Escape on the top modal', () => withFakeDocument((documentTarget) => {
  const manager = createModalLifecycleManager();
  const trigger = documentTarget.createElement();
  const lowerButton = documentTarget.createElement();
  const upperButton = documentTarget.createElement();
  let lowerCloseCount = 0;
  let upperCloseCount = 0;

  trigger.focus();
  const cleanupLower = manager.activate({
    getOnClose: () => () => { lowerCloseCount += 1; },
    initialFocusElement: lowerButton
  });
  const cleanupUpper = manager.activate({
    getOnClose: () => () => { upperCloseCount += 1; },
    initialFocusElement: upperButton
  });

  lowerButton.connected = false;
  cleanupLower();
  documentTarget.dispatchKey('Escape');

  assert.equal(documentTarget.activeElement, upperButton);
  assert.equal(upperCloseCount, 1);
  assert.equal(lowerCloseCount, 0);

  cleanupUpper();
  assert.equal(documentTarget.activeElement, trigger);
}));

test('nested modals keep the app scroll locked until the final modal closes', () => withFakeDocument((documentTarget) => {
  const manager = createModalLifecycleManager();
  documentTarget.scrollTarget.style.overflow = 'auto';

  const cleanupLower = manager.activate({
    getOnClose: () => undefined,
    initialFocusElement: documentTarget.createElement()
  });
  const cleanupUpper = manager.activate({
    getOnClose: () => undefined,
    initialFocusElement: documentTarget.createElement()
  });

  assert.equal(documentTarget.scrollTarget.style.overflow, 'hidden');
  assert.equal(documentTarget.scrollTarget.dataset.scrollLockCount, '2');

  cleanupLower();
  assert.equal(documentTarget.scrollTarget.style.overflow, 'hidden');
  assert.equal(documentTarget.scrollTarget.dataset.scrollLockCount, '1');

  cleanupUpper();
  assert.equal(documentTarget.scrollTarget.style.overflow, 'auto');
  assert.equal(documentTarget.scrollTarget.dataset.scrollLockCount, undefined);
}));

test('closing nested modals restores focus one layer at a time', () => withFakeDocument((documentTarget) => {
  const manager = createModalLifecycleManager();
  const pageTrigger = documentTarget.createElement();
  const lowerButton = documentTarget.createElement();
  const upperButton = documentTarget.createElement();

  pageTrigger.focus();
  const cleanupLower = manager.activate({
    getOnClose: () => undefined,
    initialFocusElement: lowerButton
  });
  const cleanupUpper = manager.activate({
    getOnClose: () => undefined,
    initialFocusElement: upperButton
  });

  assert.equal(documentTarget.activeElement, upperButton);
  cleanupUpper();
  assert.equal(documentTarget.activeElement, lowerButton);
  cleanupLower();
  assert.equal(documentTarget.activeElement, pageTrigger);
}));

test('焦点限制支持正反向 Tab 循环，内部顺序仍由浏览器处理', () => withFakeDocument((documentTarget) => {
  const manager = createModalLifecycleManager();
  const dialog = documentTarget.createElement();
  const [cancel, confirm] = [documentTarget.createElement(), documentTarget.createElement()];
  dialog.children = [cancel, confirm];
  const cleanup = manager.activate({ initialFocusElement: dialog, focusTrapElement: dialog });

  assert.equal(documentTarget.dispatchKey('Tab').defaultPrevented, true);
  assert.equal(documentTarget.activeElement, cancel);
  assert.equal(documentTarget.dispatchKey('Tab').defaultPrevented, false);
  assert.equal(documentTarget.dispatchKey('Tab', true).defaultPrevented, true);
  assert.equal(documentTarget.activeElement, confirm);
  assert.equal(documentTarget.dispatchKey('Tab').defaultPrevented, true);
  assert.equal(documentTarget.activeElement, cancel);
  cleanup();
}));

test('Tab 循环跳过禁用、隐藏、inert 和负 tabindex 控件', () => withFakeDocument((documentTarget) => {
  const dialog = documentTarget.createElement();
  const children = Array.from({ length: 6 }, () => documentTarget.createElement());
  children[0].disabled = true;
  children[2].visible = false;
  children[3].inert = true;
  children[5].tabIndex = -1;
  dialog.children = children;
  const cleanup = createModalLifecycleManager().activate({ initialFocusElement: dialog, focusTrapElement: dialog });

  documentTarget.dispatchKey('Tab');
  assert.equal(documentTarget.activeElement, children[1]);
  documentTarget.dispatchKey('Tab', true);
  assert.equal(documentTarget.activeElement, children[4]);
  documentTarget.dispatchKey('Tab');
  assert.equal(documentTarget.activeElement, children[1]);
  cleanup();
}));

test('无可操作控件时保留对话框焦点，并在关闭后恢复触发控件', () => withFakeDocument((documentTarget) => {
  const trigger = documentTarget.createElement();
  const dialog = documentTarget.createElement();
  trigger.focus();
  const cleanup = createModalLifecycleManager().activate({ initialFocusElement: dialog, focusTrapElement: dialog });

  for (const shift of [false, true]) {
    assert.equal(documentTarget.dispatchKey('Tab', shift).defaultPrevented, true);
    assert.equal(documentTarget.activeElement, dialog);
  }
  cleanup();
  assert.equal(documentTarget.activeElement, trigger);
  assert.equal(documentTarget.listeners.get('focusin').length, 0);
  assert.equal(documentTarget.listeners.get('keydown').length, 0);
}));

test('游离焦点被收回，关闭后背景控件恢复可聚焦', () => withFakeDocument((documentTarget) => {
  const dialog = documentTarget.createElement();
  const cancel = documentTarget.createElement();
  const background = documentTarget.createElement();
  dialog.children = [cancel];
  const cleanup = createModalLifecycleManager().activate({ initialFocusElement: dialog, focusTrapElement: dialog });
  background.focus();
  assert.equal(documentTarget.activeElement, cancel);
  cleanup();
  background.focus();
  assert.equal(documentTarget.activeElement, background);
}));

test('嵌套确认框只由最顶层限制焦点，Escape 关闭后恢复下层', () => withFakeDocument((documentTarget) => {
  const manager = createModalLifecycleManager();
  const [lower, upper, lowerButton, upperButton] = Array.from({ length: 4 }, () => documentTarget.createElement());
  lower.children = [lowerButton];
  upper.children = [upperButton];
  let lowerCloses = 0;
  const cleanupLower = manager.activate({
    initialFocusElement: lowerButton, focusTrapElement: lower, getOnClose: () => () => { lowerCloses++; }
  });
  const cleanupUpper = manager.activate({
    initialFocusElement: upperButton, focusTrapElement: upper, getOnClose: () => () => cleanupUpper()
  });

  lowerButton.focus();
  assert.equal(documentTarget.activeElement, upperButton);
  assert.equal(documentTarget.dispatchKey('Tab').defaultPrevented, true);
  assert.equal(documentTarget.activeElement, upperButton);
  documentTarget.dispatchKey('Escape');
  assert.equal(lowerCloses, 0);
  assert.equal(documentTarget.activeElement, lowerButton);
  assert.equal(documentTarget.dispatchKey('Tab').defaultPrevented, true);
  cleanupLower();
}));
