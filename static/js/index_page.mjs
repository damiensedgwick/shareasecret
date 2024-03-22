import { clearAndHideNotifications, encrypt, showErrorNotification } from "./core.mjs";

document.addEventListener("DOMContentLoaded", function() {
    const createSecretForm = document.getElementById("createSecretForm");
    if (!createSecretForm) {
        return;
    }

    createSecretForm
        .querySelector("button")
        .addEventListener("click", async function (e) {
            e.preventDefault();

            clearAndHideNotifications(createSecretForm);

            const plaintextSecret = createSecretForm.querySelector("textarea[name=plaintextSecret]").value;
            const password = createSecretForm.querySelector("input[name=password]").value;

            const encryptedSecret = await encrypt(plaintextSecret, password);

            const requestData = new URLSearchParams();
            requestData.append("ttl", createSecretForm.querySelector("select[name=ttl]").value);
            requestData.append("encryptedSecret", encryptedSecret);

            const response = await fetch(
                "/secret",
                {
                    method: "POST",
                    body: requestData,
                }
            );

            if (response.status === 201) {
                window.location.href = response.headers.get("Location");
            } else {
                showErrorNotification(createSecretForm, await response.text());
            }
        });
});
