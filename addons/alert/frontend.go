// SPDX-License-Identifier: GPL-2.0-or-later

package alert

import (
	"fmt"
	"nvr"
	"os"
	"strings"
)

func init() {
	nvr.RegisterTplHook(modifyTemplates)
}

func modifyTemplates(pageFiles map[string]string) error {
	js, exists := pageFiles["settings.js"]
	if !exists {
		return fmt.Errorf("timeline: settings.js: %w", os.ErrNotExist)
	}
	pageFiles["settings.js"] = modifySettingsjs(js)
	return nil
}

func modifySettingsjs(tpl string) string { //nolint:funlen
	const target = "logLevel: fieldTemplate.select("

	const javascript = `
	alert: (() => {
		const fields = {
			enable: fieldTemplate.toggle("Enable", "false"),
			threshold: newField(
				[inputRules.notEmpty, inputRules.noSpaces],
				{
					errorField: true,
					input: "number",
					min: "0",
					max: "100",
				},
				{
					label: "Threshold",
					placeholder: "50",
					initial: "50",
				}
			),
			cooldown: fieldTemplate.integer(
				"Cooldown (min)",
				"30",
				"30",
			),
		};
		const form = newForm(fields);
		const modal = newModal("Alert", form.html());

		let value = {};

		let isRendered = false;
		const render = (element) => {
			if (isRendered) {
				return;
			}
			element.insertAdjacentHTML("beforeend", modal.html)
			element.querySelector(".js-modal").style.maxWidth = "12rem";

			const $modalContent = modal.init(element)
			form.init($modalContent);

			modal.onClose(() => {
				// Get value.
				for (const key of Object.keys(form.fields)) {
					value[key] = form.fields[key].value();
				}
			});

			isRendered = true;
		}

		const update = () => {
			// Set value.
			for (const key of Object.keys(form.fields)) {
				if (form.fields[key] && form.fields[key].set) {
					if (value[key]) {
						form.fields[key].set(value[key]);
					} else {
						form.fields[key].set("");
					}
				}
			}
		}

		const id = uniqueID()

		return {
			html: ` + "`" + `
				<li id="${id}" class="form-field" style="display:flex;">
					<label class="form-field-label">Alert</label>
					<div>
						<button class="form-field-edit-btn" style="background: var(--color3);">
							<img src="static/icons/feather/edit-3.svg"/>
						</button>
					</div>
				</li> ` + "`" + `,
			value() {
				return JSON.stringify(value);
			},
			set(input) {
				if (input) {
					value = JSON.parse(input);
				} else {
					value = {};
				}
			},
			validate() {
				if (!isRendered) {
					return "";
				}
				const err = form.validate()
				if (err != "") {
					return "Alert: " + err;
				}
				return "";
			},
			init($parent) {
				const element = $parent.querySelector("#"+id)
				element.querySelector(".form-field-edit-btn").addEventListener("click", () => {
					render(element)
					update()
					modal.open()
				});
			},
		}
	})(),`

	return strings.ReplaceAll(tpl, target, javascript+target)
}
