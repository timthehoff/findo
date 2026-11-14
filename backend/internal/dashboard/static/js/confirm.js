// Sensitive changes (passkeys, users, owners) need a passkey confirmation from
// the last few minutes. withConfirm runs an action and, if the server asks for
// one, shows a dialog whose button opens the passkey prompt: browsers want a
// click for each prompt, so a prompt is never opened behind the person's back.
import { reauthenticate, passkeysSupported, friendlyError } from './webauthn.js';
import { toast } from './toast.js';

// withConfirm(action) runs action(); when it needs a confirmation it asks, then
// runs action() once more. With {retry: false} (for an action that opens its own
// passkey prompt) it stops after confirming and returns undefined, and the
// person presses the button again.
export async function withConfirm(action, { retry = true } = {}) {
  try {
    return await action();
  } catch (e) {
    if (!e.reauthRequired) throw e;
    await askToConfirm();
    if (!retry) {
      toast('Confirmed. Press the button again.', 'success');
      return undefined;
    }
    return action();
  }
}

function askToConfirm() {
  return new Promise((resolve, reject) => {
    const dialog = document.createElement('dialog');
    dialog.innerHTML = `
      <form method="dialog">
        <h2 style="margin:0 0 0.25rem">Confirm it's you</h2>
        <p>This changes who can get into the account, so confirm with your passkey first.</p>
        <div class="auth-msg" role="status"></div>
        <div class="dialog-actions">
          <button type="button" data-cancel>Cancel</button>
          <button type="button" class="primary" data-go>Confirm with passkey</button>
        </div>
      </form>`;
    document.body.appendChild(dialog);
    const msg = dialog.querySelector('.auth-msg');
    const go = dialog.querySelector('[data-go]');
    let done = false;
    const finish = (ok, err) => {
      if (done) return;
      done = true;
      dialog.close();
      dialog.remove();
      ok ? resolve() : reject(err);
    };

    if (!passkeysSupported()) {
      msg.className = 'auth-msg bad';
      msg.textContent = 'This browser can\'t use passkeys.';
      go.disabled = true;
    }
    go.addEventListener('click', async () => {
      go.disabled = true;
      msg.className = 'auth-msg muted';
      msg.textContent = 'Waiting for your passkey…';
      try {
        await reauthenticate();
        finish(true);
      } catch (e) {
        msg.className = 'auth-msg bad';
        msg.textContent = friendlyError(e);
        go.disabled = false;
      }
    });
    dialog.querySelector('[data-cancel]').addEventListener('click', () => finish(false, new Error('Not confirmed.')));
    dialog.addEventListener('cancel', () => finish(false, new Error('Not confirmed.')));
    dialog.showModal();
  });
}
